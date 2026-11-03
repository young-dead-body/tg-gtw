package app

import (
	"log"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/ua"
)

// screenResolutionPattern validates the X-Screen-Resolution header, e.g.
// "1920x1080" (ТЗ §12: разрешение экрана).
var screenResolutionPattern = regexp.MustCompile(`^\d{2,5}x\d{2,5}$`)

// recordLoginStatistics writes a statistics row for every login attempt
// (ТЗ §12): IP, geo, language, UA + parsed browser/OS/device, screen
// resolution, login time and the auth_error for failed attempts. Recording
// must never break the login flow, so failures are only logged.
func (s *Server) recordLoginStatistics(c *fiber.Ctx, userID pgtype.UUID, authError string) {
	ip, err := netip.ParseAddr(c.IP())
	if err != nil {
		ip = netip.Addr{}
	}

	country, region, city := "", "", ""
	if s.Geo != nil && ip.IsValid() {
		country, region, city = s.Geo.Lookup(ip)
	}

	rawUA := c.Get(fiber.HeaderUserAgent)
	parsed := ua.Parse(rawUA)

	screen := ""
	if v := c.Get("X-Screen-Resolution"); screenResolutionPattern.MatchString(v) {
		screen = v
	}

	if _, err := s.Queries.InsertStatistics(c.Context(), db.InsertStatisticsParams{
		UserID:           userID,
		Ip:               ipPtr(ip),
		Country:          pgtext(country),
		Region:           pgtext(region),
		City:             pgtext(city),
		Language:         pgtext(acceptLanguage(c.Get(fiber.HeaderAcceptLanguage))),
		UserAgent:        pgtext(rawUA),
		Browser:          pgtext(parsed.Browser),
		Os:               pgtext(parsed.OS),
		Device:           pgtext(parsed.Device),
		ScreenResolution: pgtext(screen),
		LoginAt:          pgtype.Timestamptz{Time: time.Now(), Valid: true},
		AuthError:        pgtext(authError),
	}); err != nil {
		log.Printf("stats: insert: %v", err)
	}

	// Audit trail for the same event (S13): "кто, что, когда, IP, UA".
	action := auditLoginSuccess
	if authError != "" {
		action = auditLoginFailed
	}
	s.recordAudit(c, userID, action, "user", userID.String(), map[string]any{
		"auth_error": authError,
	})
}

// ipPtr converts an addr to a pointer, returning nil for invalid addrs.
func ipPtr(ip netip.Addr) *netip.Addr {
	if !ip.IsValid() {
		return nil
	}
	return &ip
}

// acceptLanguage returns the first language tag of an Accept-Language header.
func acceptLanguage(header string) string {
	if header == "" {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(header, ",", 2)[0])
}

type statsItem struct {
	ID                     int64     `json:"id"`
	UserID                 string    `json:"user_id"`
	IP                     string    `json:"ip"`
	Country                string    `json:"country"`
	Region                 string    `json:"region"`
	City                   string    `json:"city"`
	Language               string    `json:"language"`
	UserAgent              string    `json:"user_agent"`
	Browser                string    `json:"browser"`
	OS                     string    `json:"os"`
	Device                 string    `json:"device"`
	ScreenResolution       string    `json:"screen_resolution"`
	LoginAt                time.Time `json:"login_at"`
	SessionDurationSeconds *int32    `json:"session_duration_seconds"`
	AuthError              string    `json:"auth_error"`
}

func toStatsItem(s db.Statistic) statsItem {
	item := statsItem{
		ID:               s.ID,
		UserID:           s.UserID.String(),
		Country:          s.Country.String,
		Region:           s.Region.String,
		City:             s.City.String,
		Language:         s.Language.String,
		UserAgent:        s.UserAgent.String,
		Browser:          s.Browser.String,
		OS:               s.Os.String,
		Device:           s.Device.String,
		ScreenResolution: s.ScreenResolution.String,
		LoginAt:          s.LoginAt.Time,
		AuthError:        s.AuthError.String,
	}
	if s.Ip != nil {
		item.IP = s.Ip.String()
	}
	if s.SessionDurationSeconds.Valid {
		dur := s.SessionDurationSeconds.Int32
		item.SessionDurationSeconds = &dur
	}
	return item
}

// handleListStats is the admin endpoint GET /stats (ТЗ §15) with optional
// user_id filter and pagination.
func (s *Server) handleListStats(c *fiber.Ctx) error {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	var userID pgtype.UUID
	if uid := c.Query("user_id"); uid != "" {
		id, err := uuidFromString(uid)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid user_id"})
		}
		userID = id
	}

	rows, err := s.Queries.GetStatistics(c.Context(), db.GetStatisticsParams{
		Column1: userID,
		Limit:   int32(limit),
		Offset:  int32(offset),
	})
	if err != nil {
		log.Printf("stats: list: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	items := make([]statsItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, toStatsItem(r))
	}
	return c.JSON(fiber.Map{"items": items, "limit": limit, "offset": offset})
}
