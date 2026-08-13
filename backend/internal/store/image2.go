package store

import (
	"database/sql"
	"errors"
)

const metaImage2Settings = "image2_settings"

var (
	ErrImage2SlugExists       = errors.New("image2 URL 标识已存在")
	ErrImage2UpstreamNotFound = errors.New("image2 上游不存在")
)

type Image2Settings struct {
	ImageDomain    string `json:"image_domain"`
	RetentionHours int    `json:"retention_hours"`
	ProxyAPIKey    string `json:"proxy_api_key"`
}

func DefaultImage2Settings() Image2Settings {
	return Image2Settings{RetentionHours: 24}
}

type Image2Upstream struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"-"`
	HasAPIKey      bool   `json:"has_api_key"`
	ModelMapping   string `json:"model_mapping"`
	BlockedParams  string `json:"blocked_params"`
	ProxyImageURLs bool   `json:"proxy_image_urls"`
}

func (s *Store) Image2Settings() (Image2Settings, error) {
	settings := DefaultImage2Settings()
	if err := s.getJSON(metaImage2Settings, &settings); err != nil {
		if IsNotFound(err) {
			return settings, nil
		}
		return Image2Settings{}, err
	}
	if settings.RetentionHours <= 0 {
		settings.RetentionHours = DefaultImage2Settings().RetentionHours
	}
	return settings, nil
}

func (s *Store) SaveImage2Settings(settings Image2Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setJSON(metaImage2Settings, settings)
}

func (s *Store) Image2Upstreams() ([]Image2Upstream, error) {
	rows, err := s.db.Query(`SELECT id, name, slug, base_url, api_key, model_mapping, blocked_params,
		proxy_image_urls
		FROM image2_upstreams ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var upstreams []Image2Upstream
	for rows.Next() {
		upstream, err := scanImage2Upstream(rows)
		if err != nil {
			return nil, err
		}
		upstreams = append(upstreams, upstream)
	}
	return upstreams, rows.Err()
}

func (s *Store) Image2UpstreamByID(id int64) (Image2Upstream, error) {
	return scanImage2Upstream(s.db.QueryRow(`SELECT id, name, slug, base_url, api_key,
		model_mapping, blocked_params, proxy_image_urls FROM image2_upstreams WHERE id = ?`, id))
}

func (s *Store) Image2UpstreamBySlug(slug string) (Image2Upstream, error) {
	return scanImage2Upstream(s.db.QueryRow(`SELECT id, name, slug, base_url, api_key,
		model_mapping, blocked_params, proxy_image_urls FROM image2_upstreams WHERE slug = ?`, slug))
}

func (s *Store) CreateImage2Upstream(upstream Image2Upstream) (Image2Upstream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	taken, err := s.image2SlugTaken(upstream.Slug, 0)
	if err != nil {
		return Image2Upstream{}, err
	}
	if taken {
		return Image2Upstream{}, ErrImage2SlugExists
	}
	result, err := s.db.Exec(`INSERT INTO image2_upstreams(
		name, slug, base_url, api_key, model_mapping, blocked_params, proxy_image_urls)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		upstream.Name, upstream.Slug, upstream.BaseURL, upstream.APIKey, upstream.ModelMapping,
		upstream.BlockedParams, upstream.ProxyImageURLs)
	if err != nil {
		return Image2Upstream{}, err
	}
	upstream.ID, err = result.LastInsertId()
	upstream.HasAPIKey = upstream.APIKey != ""
	return upstream, err
}

func (s *Store) UpdateImage2Upstream(upstream Image2Upstream) (Image2Upstream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	taken, err := s.image2SlugTaken(upstream.Slug, upstream.ID)
	if err != nil {
		return Image2Upstream{}, err
	}
	if taken {
		return Image2Upstream{}, ErrImage2SlugExists
	}
	result, err := s.db.Exec(`UPDATE image2_upstreams SET name = ?, slug = ?, base_url = ?,
		api_key = ?, model_mapping = ?, blocked_params = ?, proxy_image_urls = ? WHERE id = ?`,
		upstream.Name, upstream.Slug, upstream.BaseURL, upstream.APIKey, upstream.ModelMapping,
		upstream.BlockedParams, upstream.ProxyImageURLs, upstream.ID)
	if err != nil {
		return Image2Upstream{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Image2Upstream{}, err
	}
	if rows == 0 {
		return Image2Upstream{}, ErrImage2UpstreamNotFound
	}
	upstream.HasAPIKey = upstream.APIKey != ""
	return upstream, nil
}

func (s *Store) DeleteImage2Upstream(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(`DELETE FROM image2_upstreams WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrImage2UpstreamNotFound
	}
	return nil
}

func (s *Store) image2SlugTaken(slug string, exceptID int64) (bool, error) {
	var found int64
	err := s.db.QueryRow(`SELECT id FROM image2_upstreams WHERE slug = ? AND id != ?`,
		slug, exceptID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

type image2Scanner interface {
	Scan(dest ...any) error
}

func scanImage2Upstream(row image2Scanner) (Image2Upstream, error) {
	var upstream Image2Upstream
	err := row.Scan(&upstream.ID, &upstream.Name, &upstream.Slug, &upstream.BaseURL,
		&upstream.APIKey, &upstream.ModelMapping, &upstream.BlockedParams, &upstream.ProxyImageURLs)
	if errors.Is(err, sql.ErrNoRows) {
		return Image2Upstream{}, ErrImage2UpstreamNotFound
	}
	upstream.HasAPIKey = upstream.APIKey != ""
	return upstream, err
}
