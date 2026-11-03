// Package app contains the bot's business logic, independent of the Telegram
// transport so it can be unit-tested with fakes (T11.4).
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/company/telegram-gateway/bot/internal/api"
	"github.com/company/telegram-gateway/bot/internal/events"
)

const (
	eventAccessExpiring   = "access.expiring"
	eventAccessExpired    = "access.expired"
	eventPasswordExpiring = "password.expiring"
	eventPasswordExpired  = "password.expired"
	eventPasswordChanged  = "password.changed"
)

// GatewayAPI is the subset of the gateway API the bot needs.
type GatewayAPI interface {
	IssueCredentials(ctx context.Context, telegram string) (*api.Credentials, error)
	GetUser(ctx context.Context, id string) (*api.User, error)
	BindTelegram(context.Context, int64, string) error
}

// Sender delivers messages to a chat.
type Sender interface {
	Send(chatID int64, text string) error
}

// Service implements command handling and event delivery (ТЗ §9).
type Service struct {
	api    GatewayAPI
	sender Sender
}

// NewService builds the bot service. SetSender must be called before sending.
func NewService(gateway GatewayAPI) *Service {
	return &Service{api: gateway}
}

// SetSender wires the message-delivery adapter.
func (s *Service) SetSender(sender Sender) {
	s.sender = sender
}

// Register wires the telebot command handlers.
func (s *Service) Register(b *tele.Bot) {
	b.Handle("/start", s.handleStart)
	b.Handle("/help", s.handleHelp)
	b.Handle("/credentials", s.handleCredentials)
	b.Handle("/link", s.handleLink)
}

func (s *Service) handleStart(c tele.Context) error {
	if c.Chat().Type != tele.ChatPrivate {
		return c.Reply("Откройте личный чат с ботом.")
	}

	return c.Reply(s.startText(c.Sender().FirstName))
}

func (s *Service) handleHelp(c tele.Context) error {
	return c.Reply("Доступные команды:\n" +
		"/link <код> — привязать аккаунт\n" +
		"/credentials — получить ссылку установки пароля\n" +
		"/help — список команд")
}

func (s *Service) handleCredentials(c tele.Context) error {
	if c.Chat().Type != tele.ChatPrivate {
		return c.Reply("Откройте личный чат с ботом для получения ссылки.")
	}
	return c.Reply(s.issueCredentials(context.Background(), strconv.FormatInt(c.Sender().ID, 10), c.Sender().ID))
}
func (s *Service) handleLink(c tele.Context) error {
	if c.Chat().Type != tele.ChatPrivate {
		return c.Reply("Привязка доступна только в личном чате.")
	}
	args := c.Args()
	if len(args) != 1 || len(args[0]) != 64 {
		return c.Reply("Используйте /link <код из личного кабинета или от администратора>.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.api.BindTelegram(ctx, c.Sender().ID, args[0]); err != nil {
		return c.Reply("Код недействителен, истёк или аккаунт уже привязан. Получите новый код.")
	}
	return c.Reply("Аккаунт привязан. Команда /credentials выдаст одноразовую ссылку установки пароля.")
}

// startText builds the /start greeting.
func (s *Service) startText(firstName string) string {
	var sb strings.Builder
	if firstName != "" {
		sb.WriteString("Привет, " + firstName + "!\n\n")
	} else {
		sb.WriteString("Привет!\n\n")
	}
	sb.WriteString("Это бот корпоративного шлюза доступа к Telegram Web.\n")
	sb.WriteString("Привяжите аккаунт командой /link <код>. Команда /credentials выдаст одноразовую ссылку установки пароля.")
	return sb.String()
}

// issueCredentials requests fresh credentials from the gateway and returns the
// reply text. Every outcome maps to a user-facing message.
func (s *Service) issueCredentials(ctx context.Context, username string, chatID int64) string {

	creds, err := s.api.IssueCredentials(ctx, username)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Status {
			case http.StatusNotFound:
				return "Привязка аккаунта не найдена. Используйте /link с кодом от администратора. " +
					"Зарегистрируйтесь через веб-интерфейс или обратитесь к администратору."
			case http.StatusForbidden:
				return "Учётная запись отключена или заблокирована. Обратитесь к администратору."
			case http.StatusConflict:
				return "Не удалось выдать ссылку. Повторите попытку."
			}
		}
		log.Printf("bot: credentials for %s: %v", username, err)
		return "Внутренняя ошибка. Попробуйте позже."
	}

	return "Установите пароль по одноразовой ссылке (действует 15 минут):\n" + creds.ResetURL +
		"\nНе передавайте ссылку третьим лицам. Новый запрос отменяет предыдущую ссылку."

}

// HandleEvent delivers a business event to the affected user (ТЗ §9:
// уведомления об окончании доступа и смене пароля).
func (s *Service) HandleEvent(ctx context.Context, e events.Event) error {
	switch e.Name {
	case eventAccessExpiring, eventAccessExpired,
		eventPasswordExpiring, eventPasswordExpired, eventPasswordChanged:
	default:
		return nil
	}

	u, err := s.api.GetUser(ctx, e.UserID)
	if err != nil {
		return fmt.Errorf("resolve user %s: %w", e.UserID, err)
	}
	if u.TelegramUserID <= 0 {
		log.Printf("bot: user %s has no verified Telegram binding", e.UserID)
		return nil
	}
	chatID := u.TelegramUserID

	if s.sender == nil {
		return fmt.Errorf("sender not configured")
	}
	return s.sender.Send(chatID, messageFor(e.Name, u))
}

// messageFor renders the user-facing text for a business event.
func messageFor(name string, u *api.User) string {
	switch name {
	case eventAccessExpiring:
		return "Ваш доступ к Telegram Web скоро закончится" + expirySuffix(u) +
			". Продлите доступ у администратора."
	case eventAccessExpired:
		return "Ваш доступ к Telegram Web истёк" + expirySuffix(u) +
			". Обратитесь к администратору для продления."
	case eventPasswordExpiring:
		return "Срок действия вашего пароля истекает. Получите новые учётные данные командой /credentials."
	case eventPasswordExpired:
		return "Срок действия вашего пароля истёк. Получите новые учётные данные командой /credentials."
	case eventPasswordChanged:
		return "Ваш пароль был изменён. Если вы не запрашивали смену пароля, обратитесь к администратору."
	default:
		return ""
	}
}

func expirySuffix(u *api.User) string {
	if u != nil && u.ExpiresAt != nil {
		return " " + u.ExpiresAt.Format("02.01.2006")
	}
	return ""
}

// normalizeHandle strips a leading '@' and lowercases the handle.
func normalizeHandle(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "@"))
}
