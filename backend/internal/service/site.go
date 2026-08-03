package service

import (
	"context"
	"strings"

	"backend/internal/repo"
	"backend/internal/storage"
)

type SiteService struct {
	settings *repo.SiteSettingRepository
	fallback string
	store    *storage.Client
}

func NewSiteService(settings *repo.SiteSettingRepository, fallback string, store *storage.Client) *SiteService {
	return &SiteService{
		settings: settings,
		fallback: fallback,
		store:    store,
	}
}

func (s *SiteService) Title(ctx context.Context) (string, error) {
	v, err := s.settings.GetValue(ctx, "site.title")
	if err != nil {
		return "", err
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return s.fallback, nil
	}
	return v, nil
}

func (s *SiteService) SetTitle(ctx context.Context, title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", nil
	}
	if err := s.settings.UpsertValue(ctx, "site.title", title); err != nil {
		return "", err
	}
	return title, nil
}

func (s *SiteService) get(ctx context.Context, key string) string {
	v, _ := s.settings.GetValue(ctx, key)
	return strings.TrimSpace(v)
}

// Logo / Subtitle are admin-editable branding shown on the public site.
func (s *SiteService) Logo(ctx context.Context) string     { return s.get(ctx, "site.logo") }
func (s *SiteService) Subtitle(ctx context.Context) string { return s.get(ctx, "site.subtitle") }
func (s *SiteService) MediaBaseURL() string {
	if s.store == nil {
		return ""
	}
	return s.store.PublicBaseURL()
}

// CDKRedeemEnabled reflects the admin "兑换码" switch (default on). The front-end
// hides the redeem UI when off.
func (s *SiteService) CDKRedeemEnabled(ctx context.Context) bool {
	return s.get(ctx, "credits.cdk_redeem_enabled") != "false"
}

// SetSubtitle persists the homepage 子标题. (The logo is managed separately via
// the upload/delete endpoints so a site-form save never clobbers it.)
func (s *SiteService) SetSubtitle(ctx context.Context, subtitle string) error {
	return s.settings.UpsertValue(ctx, "site.subtitle", strings.TrimSpace(subtitle))
}

// Contact is the admin-editable "联系我们" info shown in the public 关于 section.
type Contact struct {
	QQ          string `json:"qq"`
	QQLink      string `json:"qq_link"`
	QQGroup     string `json:"qq_group"`
	QQGroupLink string `json:"qq_group_link"`
	Email       string `json:"email"`
	Shop        string `json:"shop"`
}

func (s *SiteService) Contact(ctx context.Context) Contact {
	get := func(k string) string { v, _ := s.settings.GetValue(ctx, k); return strings.TrimSpace(v) }
	return Contact{
		QQ:          get("contact.qq"),
		QQLink:      get("contact.qq_link"),
		QQGroup:     get("contact.qq_group"),
		QQGroupLink: get("contact.qq_group_link"),
		Email:       get("contact.email"),
		Shop:        get("contact.shop"),
	}
}

func (s *SiteService) SetContact(ctx context.Context, c Contact) error {
	for k, v := range map[string]string{
		"contact.qq":            strings.TrimSpace(c.QQ),
		"contact.qq_link":       strings.TrimSpace(c.QQLink),
		"contact.qq_group":      strings.TrimSpace(c.QQGroup),
		"contact.qq_group_link": strings.TrimSpace(c.QQGroupLink),
		"contact.email":         strings.TrimSpace(c.Email),
		"contact.shop":          strings.TrimSpace(c.Shop),
	} {
		if err := s.settings.UpsertValue(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}
