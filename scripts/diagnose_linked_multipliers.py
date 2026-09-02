#!/usr/bin/env python3
"""Read-only diagnosis for channel-management multiplier linking.

This script intentionally never prints URLs, API keys, passwords, cache JSON, or
event details. It only reports counts, timestamps, IDs, and safe error messages.
It uses Python's standard library only.
"""

import argparse
import datetime as dt
import json
import math
import os
import re
import sqlite3
import sys
from collections import Counter, defaultdict


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
GROUP_TASKS = {"group_ratio_changed", "group_added", "group_removed"}
LINK_ACTION_PREFIX = "upstream_multiplier_link"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="只读诊断 Guardian 渠道管理倍率联动，不输出凭据"
    )
    parser.add_argument(
        "db",
        nargs="?",
        help="SQLite 路径；省略时自动尝试 /data/guardian.sqlite 和常见相对路径",
    )
    parser.add_argument(
        "--latest-events",
        type=int,
        default=30,
        help="显示最近多少条安全事件摘要（默认 30，设 0 可关闭）",
    )
    return parser.parse_args()


def choose_db(explicit=None):
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
    connection = sqlite3.connect(uri, uri=True, timeout=3)
    connection.row_factory = sqlite3.Row
    connection.execute("PRAGMA query_only=ON")
    return connection


def table_exists(db, name):
    row = db.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (name,)
    ).fetchone()
    return row is not None


def parse_json(raw, default):
    if raw is None:
        return default
    try:
        return json.loads(raw)
    except (TypeError, ValueError):
        return default


def as_object(value):
    return value if isinstance(value, dict) else None


def collection(value):
    if isinstance(value, list):
        return value
    if not isinstance(value, dict):
        return []
    for field in ("items", "data", "rows", "list", "tokens", "keys"):
        if isinstance(value.get(field), list):
            return value[field]
    return list(value.values())


def text(value):
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value).strip()


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


def first(record, fields):
    if not record:
        return None
    for field in fields:
        if field in record:
            return record[field]
    return None


def key_value(token):
    for field in KEY_FIELDS:
        value = text(token.get(field))
        if value:
            return field, value
    return "", ""


def identifiers(value):
    record = as_object(value)
    if record is None:
        value_text = text(value)
        return {value_text.lower()} if value_text else set()
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
                result.add(value_text.lower())
    return result


def token_group_identifiers(token):
    result = set()
    for field in GROUP_FIELDS:
        value = text(token.get(field))
        if value:
            result.add(value.lower())
    embedded = first(token, EMBEDDED_GROUP_FIELDS)
    result.update(identifiers(embedded))
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


def parse_time(value):
    raw = text(value)
    if not raw:
        return None
    try:
        parsed = dt.datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return parsed.astimezone(dt.timezone.utc)


def fmt_time(value):
    parsed = parse_time(value)
    return parsed.astimezone().strftime("%Y-%m-%d %H:%M:%S %Z") if parsed else "未记录"


def task_state(row, now):
    if not row["enabled"]:
        return "关闭"
    last = parse_time(row["last_run_at"])
    if last is None:
        return "已到期（从未运行）"
    age = (now - last).total_seconds() / 60
    interval = max(1, int(row["interval_minutes"] or 1))
    if age >= interval:
        return f"已到期（超 {age - interval:.1f} 分钟）"
    return f"约 {interval - age:.1f} 分钟后到期"


def analyze_tokens(groups_raw, tokens_raw):
    groups = collection(groups_raw)
    tokens = collection(tokens_raw)
    group_ids = [identifiers(group) for group in groups]
    full = masked = missing = with_group = valid_ratio = candidates = 0
    ratios_by_key = defaultdict(set)

    for raw_token in tokens:
        token = as_object(raw_token)
        if token is None:
            continue
        _, key = key_value(token)
        if not key:
            missing += 1
            continue
        if "*" in key:
            masked += 1
            continue
        full += 1
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
            continue
        with_group += 1
        ratio = token_ratio(matched_group, token)
        if ratio is None:
            continue
        valid_ratio += 1
        ratios_by_key[key].add(ratio)
        candidates += 1

    duplicate_keys = sum(1 for values in ratios_by_key.values() if len(values) > 1)
    return {
        "groups": len(groups),
        "tokens": len(tokens),
        "full": full,
        "masked": masked,
        "missing": missing,
        "with_group": with_group,
        "valid_ratio": valid_ratio,
        "candidates": candidates,
        "duplicate_conflicts": duplicate_keys,
        "unique_candidate_keys": len(ratios_by_key),
    }


def print_header(title: str) -> None:
    print(f"\n=== {title} ===")


def main() -> int:
    args = parse_args()
    db_path = choose_db(args.db)
    print(f"数据库（只读）: {db_path}")
    if not os.path.isfile(db_path):
        print("错误：找不到数据库文件。请把实际 guardian.sqlite 路径作为参数传入。", file=sys.stderr)
        return 2

    try:
        db = open_readonly(db_path)
    except sqlite3.Error as exc:
        print(f"错误：无法以只读方式打开 SQLite：{exc}", file=sys.stderr)
        return 2

    try:
        now = dt.datetime.now(dt.timezone.utc)
        print(f"检查时间: {now.astimezone().strftime('%Y-%m-%d %H:%M:%S %Z')}")

        if table_exists(db, "accounts_cache"):
            account_rows = db.execute("SELECT json FROM accounts_cache ORDER BY id").fetchall()
        else:
            account_rows = []
        account_types = Counter()
        account_count = suffix_count = 0
        for row in account_rows:
            account = parse_json(row["json"], {})
            if not isinstance(account, dict):
                continue
            account_count += 1
            account_types[text(account.get("type")) or "<空>"] += 1
            if "【x" in text(account.get("name")):
                suffix_count += 1

        print_header("渠道池账号")
        print(f"账号缓存总数: {account_count}")
        print(f"名称包含【x的数量: {suffix_count}")
        print("账号类型: " + ", ".join(f"{kind}={count}" for kind, count in account_types.items()))

        linked_ids = set()
        policy_row = db.execute("SELECT value FROM meta WHERE key='policy_global'").fetchone() if table_exists(db, "meta") else None
        if policy_row:
            policy = parse_json(policy_row["value"], {})
            linked = policy.get("account_linked_multipliers", {}) if isinstance(policy, dict) else {}
            if isinstance(linked, dict):
                linked_ids = {text(key) for key, value in linked.items() if positive_number(value) is not None}
        cached_ids = set()
        for row in account_rows:
            account = parse_json(row["json"], {})
            if isinstance(account, dict) and text(account.get("id")):
                cached_ids.add(text(account["id"]))
        print(f"策略中的有效联动倍率数量: {len(linked_ids)}")
        print(f"策略有联动但账号缓存不存在: {len(linked_ids - cached_ids)}")

        print_header("渠道管理任务与缓存")
        channel_rows = db.execute(
            "SELECT id, type, status, last_sync_at "
            "FROM upstream_channels ORDER BY id"
        ).fetchall() if table_exists(db, "upstream_channels") else []
        task_rows = db.execute(
            "SELECT id, channel_id, type, enabled, interval_minutes, last_run_at "
            "FROM upstream_automation_tasks ORDER BY channel_id, id"
        ).fetchall() if table_exists(db, "upstream_automation_tasks") else []
        tasks_by_channel = defaultdict(list)
        for task in task_rows:
            tasks_by_channel[int(task["channel_id"])].append(task)

        cache_rows = db.execute(
            "SELECT channel_id, cache_key, normalized_json, synced_at "
            "FROM upstream_channel_cache WHERE cache_key IN ('groups','tokens')"
        ).fetchall() if table_exists(db, "upstream_channel_cache") else []
        caches = {
            (int(row["channel_id"]), text(row["cache_key"])): row for row in cache_rows
        }

        total_candidates = 0
        stale_or_due = 0
        for channel in channel_rows:
            channel_id = int(channel["id"])
            is_linked_type = text(channel["type"]).lower() in ("sub2api", "newapi")
            tasks = tasks_by_channel.get(channel_id, [])
            group_tasks = [task for task in tasks if text(task["type"]) in GROUP_TASKS]
            enabled_group_tasks = [task for task in group_tasks if task["enabled"]]
            due_group_tasks = [task for task in enabled_group_tasks if task_state(task, now).startswith("已到期")]
            if is_linked_type and due_group_tasks:
                stale_or_due += 1
            groups_row = caches.get((channel_id, "groups"))
            tokens_row = caches.get((channel_id, "tokens"))
            groups_raw = parse_json(groups_row["normalized_json"], []) if groups_row else []
            tokens_raw = parse_json(tokens_row["normalized_json"], []) if tokens_row else []
            stats = analyze_tokens(groups_raw, tokens_raw)
            total_candidates += stats["unique_candidate_keys"]
            task_text = "；".join(
                f"{text(task['type'])}={'开' if task['enabled'] else '关'} "
                f"间隔{task['interval_minutes']}m "
                f"{task_state(task, now)} 上次{fmt_time(task['last_run_at'])}"
                for task in group_tasks
            ) or "无分组监测任务"
            cache_text = (
                f"groups={stats['groups']} tokens={stats['tokens']} "
                f"完整Key={stats['full']} 脱敏={stats['masked']} 缺失={stats['missing']} "
                f"有分组={stats['with_group']} 有效倍率={stats['valid_ratio']} "
                f"可联动Key={stats['unique_candidate_keys']} 冲突={stats['duplicate_conflicts']}"
            )
            print(
                f"渠道#{channel_id} "
                f"[{text(channel['type'])}] 状态={text(channel['status']) or '-'} "
                f"上次同步={fmt_time(channel['last_sync_at'])}"
            )
            print(f"  任务: {task_text}")
            print(f"  缓存: {cache_text}")

        print(f"可联动上游分组任务当前已到期的渠道数: {stale_or_due}")
        print(f"所有上游缓存估算的唯一可联动 Key 总数: {total_candidates}")

        print_header("联动事件")
        if table_exists(db, "events"):
            action_rows = db.execute(
                "SELECT action, COUNT(*) AS count, MAX(created_at) AS last_at "
                "FROM events WHERE action LIKE ? GROUP BY action ORDER BY last_at DESC",
                (LINK_ACTION_PREFIX + "%",),
            ).fetchall()
            if action_rows:
                for row in action_rows:
                    print(f"{text(row['action'])}: {row['count']} 条，最近 {fmt_time(row['last_at'])}")
            else:
                print("没有找到 upstream_multiplier_link* 事件。")
            if args.latest_events > 0:
                latest_rows = db.execute(
                    "SELECT created_at, action, account_id, message FROM events "
                    "WHERE action LIKE ? ORDER BY id DESC LIMIT ?",
                    (LINK_ACTION_PREFIX + "%", args.latest_events),
                ).fetchall()
                if latest_rows:
                    print("最近事件:")
                    for row in latest_rows:
                        message = text(row["message"])
                        # 联动错误消息只保留安全的错误类别/HTTP 状态，不输出原文。
                        status = re.search(r"HTTP\s+(\d{3})", message, flags=re.IGNORECASE)
                        if status:
                            message = f"HTTP {status.group(1)}"
                        elif "超时" in message or "deadline" in message.lower():
                            message = "请求超时"
                        elif "取消" in message or "canceled" in message.lower():
                            message = "请求取消"
                        elif text(row["action"]) == "upstream_multiplier_linked":
                            message = "已匹配并写回"
                        else:
                            message = "已记录（详情已隐藏）"
                        account = f"账号#{row['account_id']}" if row["account_id"] else "无账号"
                        print(f"  {fmt_time(row['created_at'])} {text(row['action'])} {account} {message}")
        else:
            print("events 表不存在。")

        print_header("初步判断")
        if suffix_count == account_count and account_count > 0:
            print("所有账号名称都已有【x后缀，联动名称部分已完成。")
        elif total_candidates == 0:
            print("缓存中没有可联动候选：优先检查分组倍率、令牌分组归属或令牌 Key 是否脱敏。")
        elif stale_or_due > 0:
            print("仍有分组任务已到期：优先检查后台任务是否持续运行，以及对应渠道是否有错误。")
        else:
            print("任务和缓存看起来已处理，但仍有账号未加后缀；重点看 API Key 精确匹配和联动失败事件。")
        print("说明：账号凭据不会写入 Guardian SQLite，因此本脚本无法也不会直接核对 API Key 原文。")
        return 0
    finally:
        db.close()


if __name__ == "__main__":
    raise SystemExit(main())
