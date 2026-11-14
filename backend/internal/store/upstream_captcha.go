package store

func (s *Store) IncrementUpstreamYesCaptchaCalls(channelID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(`UPDATE upstream_channels SET yescaptcha_calls = yescaptcha_calls + 1 WHERE id = ?`, channelID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrUpstreamChannelNotFound
	}
	return nil
}

type UpstreamCaptchaSettings struct {
	Enabled   bool   `json:"enabled"`
	ClientKey string `json:"client_key"`
}

func (s *Store) UpstreamCaptchaSettings() (UpstreamCaptchaSettings, error) {
	var settings UpstreamCaptchaSettings
	err := s.getJSON("upstream_captcha_settings", &settings)
	if IsNotFound(err) {
		err = nil
	}
	return settings, err
}

func (s *Store) SaveUpstreamCaptchaSettings(settings UpstreamCaptchaSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setJSON("upstream_captcha_settings", settings)
}
