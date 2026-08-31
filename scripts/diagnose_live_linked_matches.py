#!/usr/bin/env python3
"""Read-only live check for channel-management URL + API key matches.

The script reads Guardian's connection settings and upstream caches from SQLite,
then calls Sub2API's administrator export endpoint. Exported credentials stay
in memory for the duration of the process and are never printed, serialized, or
written to disk. Only counts, IDs, and safe status categories are displayed.
"""

import argparse
import concurrent.futures
import datetime as dt
import json
import math
import os
import sqlite3
import sys
from collections import Counter, defaultdict
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit, urlunsplit
from urllib.request import Request, urlopen


KEY_FIELDS = ("key", "Key", "api_key", "apiKey", "token")
GROUP_FIELDS = ("group_id", "groupId", "groupID", "group_name", "groupName")
EMBEDDED_GROUP_FIELDS = ("group", "Group")
EXPLICIT_RATIO_FIELDS = (
    "user_rate_multiplier",
    "userRateMultiplier",
    "custom_rate_multiplier",
    "customRateMultiplier",
)
ORDINARY_RATIO_FIELDS = (
    "ratio",
    "rate",
    "multiplier",
    "rate_multiplier",
    "rateMultiplier",
    "group_ratio",
    "model_ratio",
    "倍率",
    "value",
)


def parse_args():
    parser = argparse.ArgumentParser(
        description="只读核对 Sub2API 导出凭据与渠道管理缓存的 URL+Key 精确匹配"
    )
    parser.add_argument(
        "db",
        nargs="?",
        help="SQLite 路径；省略时自动尝试 /data/guardian.sqlite 和常见相对路径",
    )
    parser.add_argument(
        "--workers",
        type=int,
        default=8,
        help="同时读取导出凭据的账号数（默认 8，建议不要超过 16）",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=10.0,
        help="单个导出请求超时秒数（默认 10）",
    )
    return parser.parse_args()


def choose_db(explicit):
    if explicit:
        return os.path.abspath(explicit)
    candidates = (
        "/data/guardian.sqlite",
        "./backend/data/guardian.sqlite",
        "./data/guardian.sqlite",
        "./guardian.sqlite",
    )
    for candidate in candidates:
        if os.path.isfile(candidate):
            return os.path.abspath(candidate)
    return os.path.abspath(candidates[0])


def open_readonly(path):
    uri = "file:" + path.replace("\\", "/") + "?mode=ro"
    db = sqlite3.connect(uri, uri=True, timeout=3)
    db.row_factory = sqlite3.Row
    db.execute("PRAGMA query_only=ON")
    return db


def table_exists(db, name):
    return db.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (name,)
    ).fetchone() is not None


def parse_json(raw, default):
    try:
        value = json.loads(raw)
    except (TypeError, ValueError):
        return default
    return value


def text(value):
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value).strip()


def as_object(value):
    return value if isinstance(value, dict) else None


def first(record, fields):
    if not isinstance(record, dict):
        return None
    for field in fields:
        if field in record:
            return record[field]
    return None


def collection(value):
    if isinstance(value, list):
        return value
    if not isinstance(value, dict):
        return []
    for field in ("items", "data", "rows", "list", "tokens", "keys"):
        if isinstance(value.get(field), list):
            return value[field]
    return list(value.values())


def finite_number(value):
    if value is None or isinstance(value, bool):
        return None
    try:
        number = float(value)
    except (TypeError, ValueError):
        return None
    return number if math.isfinite(number) else None


def positive_number(value):
    number = finite_number(value)
    return number if number is not None and number > 0 else None


def normalize_url(raw):
    """Mirror the Go linker: lower scheme/host, trim path slash, drop query."""
    try:
        parsed = urlsplit(text(raw))
        if parsed.scheme.lower() not in ("http", "https") or not parsed.netloc:
            return None
        # Accessing hostname validates malformed ports without exposing values.
        _ = parsed.hostname
        netloc = parsed.netloc.lower()
    except (TypeError, ValueError):
        return None
    path = parsed.path.rstrip("/")
    return urlunsplit((parsed.scheme.lower(), netloc, path, "", ""))


def identifiers(value):
    record = as_object(value)
    if record is None:
        value_text = text(value)
        return {value_text} if value_text else set()
    result = set()
    fields = (
        "id",
        "ID",
        "group_id",
        "groupId",
        "groupID",
        "name",
        "Name",
        "group",
        "Group",
        "key",
        "code",
        "group_name",
        "groupName",
        "display_name",
        "displayName",
        "title",
    )
    for field in fields:
        child = record.get(field)
        nested = as_object(child)
        if nested is not None:
            result.update(identifiers(nested))
        else:
            value_text = text(child)
            if value_text:
                result.add(value_text)
    return result


def token_key(token):
    for field in KEY_FIELDS:
        value = text(token.get(field))
        if value and "*" not in value:
            return value
    return ""


def token_group_identifiers(token):
    result = set()
    for field in GROUP_FIELDS:
        value = text(token.get(field))
        if value:
            result.add(value)
    result.update(identifiers(first(token, EMBEDDED_GROUP_FIELDS)))
    return result


def ratio_from_fields(value, fields):
    record = as_object(value)
    if record is None:
        return None
    for field in fields:
        ratio = positive_number(record.get(field))
        if ratio is not None:
            return ratio
    return None


def token_ratio(group, token):
    embedded = first(token, EMBEDDED_GROUP_FIELDS)
    for candidate in (group, embedded, token):
        ratio = ratio_from_fields(candidate, EXPLICIT_RATIO_FIELDS)
        if ratio is not None:
            return ratio
    for candidate in (embedded, group, token):
        ratio = positive_number(candidate)
        if ratio is not None:
            return ratio
        ratio = ratio_from_fields(candidate, ORDINARY_RATIO_FIELDS)
        if ratio is not None:
            return ratio
    return None


def source_pairs(groups_raw, tokens_raw, source_url):
    groups = collection(groups_raw)
    group_ids = [identifiers(group) for group in groups]
    pairs = set()
    key_urls = defaultdict(set)
    key_ratios = defaultdict(set)
    stats = Counter()
    for raw_token in collection(tokens_raw):
        token = as_object(raw_token)
        if token is None:
            continue
        key = token_key(token)
        if not key:
            stats["missing_or_masked"] += 1
            continue
        stats["full_key"] += 1
        token_ids = token_group_identifiers(token)
        matched_group = None
        for index, ids in enumerate(group_ids):
            if token_ids.intersection(ids):
                matched_group = groups[index]
                break
        if matched_group is None:
            embedded = first(token, EMBEDDED_GROUP_FIELDS)
            if identifiers(embedded):
                matched_group = embedded
        if matched_group is None:
            stats["without_group"] += 1
            continue
        ratio = token_ratio(matched_group, token)
        if ratio is None:
            stats["invalid_ratio"] += 1
            continue
        stats["candidate"] += 1
        if source_url is None:
            continue
        pair = (source_url, key)
        pairs.add(pair)
        key_urls[key].add(source_url)
        key_ratios[key].add(ratio)
    return pairs, key_urls, key_ratios, stats


def connection_settings(db):
    if not table_exists(db, "meta"):
        return "", ""
    row = db.execute("SELECT value FROM meta WHERE key='connection'").fetchone()
    config = parse_json(row["value"], {}) if row else {}
    return (
        text(config.get("base_url") or config.get("baseURL")),
        text(config.get("admin_api_key") or config.get("adminAPIKey")),
    )


def linked_ids(db):
    if not table_exists(db, "meta"):
        return set()
    row = db.execute("SELECT value FROM meta WHERE key='policy_global'").fetchone()
    policy = parse_json(row["value"], {}) if row else {}
    values = policy.get("account_linked_multipliers", {}) if isinstance(policy, dict) else {}
    if not isinstance(values, dict):
        return set()
    return {
        text(account_id)
        for account_id, value in values.items()
        if positive_number(value) is not None
    }


def credentials_from_export(payload):
    # Client.request unwraps the normal envelope, but accept both forms here.
    if isinstance(payload, dict) and "data" in payload:
        payload = payload.get("data")
    if isinstance(payload, dict):
        accounts = payload.get("accounts")
    else:
        accounts = payload
    if not isinstance(accounts, list) or len(accounts) != 1:
        return None, "shape"
    account = as_object(accounts[0])
    if account is None:
        return None, "shape"
    account_type = text(account.get("type")).lower()
    if account_type and account_type not in ("apikey", "api_key", "key"):
        return None, "not_apikey"
    credentials = as_object(account.get("credentials"))
    if credentials is None:
        return None, "missing_credentials"
    api_key = text(first(credentials, KEY_FIELDS))
    base_url = text(first(credentials, ("base_url", "baseURL", "url")))
    if not api_key:
        return None, "missing_key"
    if "*" in api_key:
        return None, "masked_key"
    if not base_url:
        return None, "missing_url"
    normalized = normalize_url(base_url)
    if normalized is None:
        return None, "invalid_url"
    return (normalized, api_key), "ok"


def fetch_export(base_url, admin_key, account_id, timeout):
    path = "/api/v1/admin/accounts/data?ids=%s&include_proxies=false" % account_id
    request = Request(
        base_url.rstrip("/") + path,
        headers={"x-api-key": admin_key, "Accept": "application/json"},
        method="GET",
    )
    try:
        with urlopen(request, timeout=timeout) as response:
            raw = response.read(4 << 20)
    except HTTPError as exc:
        return account_id, None, "http_%s" % getattr(exc, "code", "error")
    except (URLError, TimeoutError, OSError):
        return account_id, None, "network_or_timeout"
    try:
        payload = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        return account_id, None, "invalid_json"
    credentials, status = credentials_from_export(payload)
    return account_id, credentials, status


def print_header(title):
    print("\n=== %s ===" % title)


def main():
    args = parse_args()
    db_path = choose_db(args.db)
    print("数据库（只读）: %s" % db_path)
    if not os.path.isfile(db_path):
        print("错误：找不到数据库文件。", file=sys.stderr)
        return 2
    try:
        db = open_readonly(db_path)
    except sqlite3.Error as exc:
        print("错误：无法以只读方式打开 SQLite：%s" % exc, file=sys.stderr)
        return 2

    try:
        base_url, admin_key = connection_settings(db)
        if not base_url or not admin_key:
            print("错误：SQLite 中没有完整的 Sub2API 地址和 Admin API Key。", file=sys.stderr)
            return 2
        normalized_admin_url = normalize_url(base_url)
        if normalized_admin_url is None:
            print("错误：SQLite 中的 Sub2API 地址不是有效的 HTTP(S) 地址。", file=sys.stderr)
            return 2

        source_pairs_set = set()
        source_pair_channels = defaultdict(set)
        source_key_urls = defaultdict(set)
        source_key_ratios = defaultdict(set)
        source_urls = set()
        source_stats = Counter()
        source_channels = 0
        channel_info = {}
        enabled_group_tasks = defaultdict(bool)
        if table_exists(db, "upstream_automation_tasks"):
            task_rows = db.execute(
                "SELECT channel_id, type, enabled FROM upstream_automation_tasks"
            ).fetchall()
            for task in task_rows:
                if text(task["type"]) in {
                    "group_ratio_changed",
                    "group_added",
                    "group_removed",
                } and bool(task["enabled"]):
                    enabled_group_tasks[int(task["channel_id"])] = True
        if table_exists(db, "upstream_channels") and table_exists(db, "upstream_channel_cache"):
            channels = db.execute(
                "SELECT id, type, base_url, ignored, status FROM upstream_channels ORDER BY id"
            ).fetchall()
            caches = db.execute(
                "SELECT channel_id, cache_key, normalized_json "
                "FROM upstream_channel_cache WHERE cache_key IN ('groups','tokens')"
            ).fetchall()
            cache_map = {(int(row["channel_id"]), text(row["cache_key"])): row for row in caches}
            for channel in channels:
                if text(channel["type"]).lower() != "sub2api":
                    continue
                source_channels += 1
                channel_id = int(channel["id"])
                channel_info[channel_id] = {
                    "ignored": bool(channel["ignored"]),
                    "status": text(channel["status"]) or "-",
                }
                channel_url = normalize_url(channel["base_url"])
                if channel_url is not None:
                    source_urls.add(channel_url)
                groups_row = cache_map.get((channel_id, "groups"))
                tokens_row = cache_map.get((channel_id, "tokens"))
                groups = parse_json(groups_row["normalized_json"], []) if groups_row else []
                tokens = parse_json(tokens_row["normalized_json"], []) if tokens_row else []
                pairs, key_urls, key_ratios, stats = source_pairs(groups, tokens, channel_url)
                source_pairs_set.update(pairs)
                for pair in pairs:
                    source_pair_channels[pair].add(channel_id)
                for key, urls in key_urls.items():
                    source_key_urls[key].update(urls)
                for key, ratios in key_ratios.items():
                    source_key_ratios[key].update(ratios)
                source_stats.update(stats)

        account_ids = []
        if table_exists(db, "accounts_cache"):
            rows = db.execute("SELECT id, json FROM accounts_cache ORDER BY id").fetchall()
            for row in rows:
                account = parse_json(row["json"], {})
                if not isinstance(account, dict):
                    continue
                account_type = text(account.get("type")).lower()
                if account_type in ("apikey", "api_key", "key"):
                    try:
                        account_id = int(account.get("id") or row["id"])
                    except (TypeError, ValueError):
                        continue
                    if account_id > 0:
                        account_ids.append(account_id)
        account_ids = sorted(set(account_ids))
        worker_count = max(1, min(int(args.workers or 1), 16, len(account_ids) or 1))
        timeout = max(1.0, float(args.timeout or 1.0))

        print_header("来源缓存候选")
        print("参与核对的 Sub2API 渠道数: %d" % source_channels)
        print("完整 Key: %d；有效分组倍率候选: %d" % (source_stats["full_key"], source_stats["candidate"]))
        print("URL+Key 候选配对数: %d" % len(source_pairs_set))
        print("候选 Key 中倍率冲突数: %d" % sum(1 for values in source_key_ratios.values() if len(values) > 1))

        print_header("Sub2API 导出凭据读取")
        print("待读取的 API Key 账号数: %d" % len(account_ids))
        print("并发数: %d；单请求超时: %.1fs" % (worker_count, timeout))
        if not account_ids:
            print("没有可读取的 API Key 账号。")
            return 0

        statuses = Counter()
        exact_ids = set()
        exact_source_channels = defaultdict(set)
        key_only_ids = set()
        url_only_ids = set()
        neither_ids = set()
        with concurrent.futures.ThreadPoolExecutor(max_workers=worker_count) as pool:
            futures = [
                pool.submit(fetch_export, base_url, admin_key, account_id, timeout)
                for account_id in account_ids
            ]
            for future in concurrent.futures.as_completed(futures):
                account_id, credentials, status = future.result()
                statuses[status] += 1
                if status != "ok" or credentials is None:
                    continue
                account_url, api_key = credentials
                if (account_url, api_key) in source_pairs_set:
                    exact_ids.add(str(account_id))
                    exact_source_channels[str(account_id)].update(
                        source_pair_channels.get((account_url, api_key), set())
                    )
                elif api_key in source_key_urls:
                    key_only_ids.add(str(account_id))
                elif account_url in source_urls:
                    url_only_ids.add(str(account_id))
                else:
                    neither_ids.add(str(account_id))

        print("导出成功: %d" % statuses["ok"])
        for status, count in sorted(statuses.items()):
            if status != "ok":
                print("%s: %d" % (status, count))

        print_header("精确匹配结果")
        linked = linked_ids(db)
        print("URL+Key 精确匹配账号数: %d" % len(exact_ids))
        print("仅 Key 相同、URL 不同: %d" % len(key_only_ids))
        print("仅 URL 相同、Key 不同: %d" % len(url_only_ids))
        print("URL 和 Key 都不同或无候选: %d" % len(neither_ids))
        print("策略中已有有效联动倍率: %d" % len(linked))
        print("精确匹配但策略尚未记录: %d" % len(exact_ids - linked))
        print("策略已有联动但本次未精确匹配: %d" % len(linked - exact_ids))
        missing_ids = sorted(exact_ids - linked, key=lambda value: int(value))
        if missing_ids:
            print("尚未写入策略的精确账号（仅显示 ID 和来源渠道状态）:")
            for account_id in missing_ids:
                channels = []
                for channel_id in sorted(exact_source_channels.get(account_id, set())):
                    info = channel_info.get(channel_id, {})
                    flags = []
                    if info.get("ignored"):
                        flags.append("已忽略")
                    if not enabled_group_tasks.get(channel_id):
                        flags.append("无启用分组任务")
                    status = info.get("status", "-")
                    if status != "active":
                        flags.append("状态=" + status)
                    channels.append("#%d(%s)" % (channel_id, "、".join(flags) or "可触发"))
                print("  账号#%s <- %s" % (account_id, ", ".join(channels) or "来源渠道未找到"))
        print("说明：凭据只在本进程内比较；本脚本不会输出、序列化或写入 URL、Key。")
        print("检查时间: %s" % dt.datetime.now().astimezone().strftime("%Y-%m-%d %H:%M:%S %Z"))
        return 0
    finally:
        db.close()


if __name__ == "__main__":
    raise SystemExit(main())
