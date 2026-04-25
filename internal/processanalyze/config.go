package processanalyze

import (
	"net/http"
	"os"
	"strings"

	"github.com/Goalt/tg-channel-to-rss/internal/gamma"
	"github.com/Goalt/tg-channel-to-rss/internal/openrouter"
	"github.com/Goalt/tg-channel-to-rss/internal/processdb"
	"github.com/Goalt/tg-channel-to-rss/internal/telegrambot"
)

type Config struct {
	OpenRouterAPIBaseURL    string
	OpenRouterAuthorization string
	OpenRouterModel         string
	GammaAPIBaseURL         string
	SQLitePath              string
	SQLiteTable             string
	TelegramAPIBaseURL      string
	TelegramBotToken        string
	TelegramChatID          string
}

func LoadConfigFromEnv(getenv func(string) string) (Config, []string) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{
		OpenRouterAPIBaseURL:    env(getenv, "PROCESS_ANALYZE_OPENROUTER_API_BASE_URL", openrouterBaseURL()),
		OpenRouterAuthorization: strings.TrimSpace(getenv("PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION")),
		OpenRouterModel:         env(getenv, "PROCESS_ANALYZE_OPENROUTER_MODEL", "tencent/hy3-preview:free"),
		GammaAPIBaseURL:         env(getenv, "PROCESS_ANALYZE_GAMMA_API_BASE_URL", gammaBaseURL()),
		SQLitePath:              env(getenv, "PROCESS_ANALYZE_SQLITE_PATH", "process-analyze.db"),
		SQLiteTable:             env(getenv, "PROCESS_ANALYZE_SQLITE_TABLE", "process_analyze_news"),
		TelegramAPIBaseURL:      env(getenv, "PROCESS_ANALYZE_TELEGRAM_API_BASE_URL", telegramBaseURL()),
		TelegramBotToken:        strings.TrimSpace(getenv("PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN")),
		TelegramChatID:          strings.TrimSpace(getenv("PROCESS_ANALYZE_TELEGRAM_CHAT_ID")),
	}
	missing := make([]string, 0)
	if cfg.OpenRouterAuthorization == "" {
		missing = append(missing, "PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION")
	}
	if cfg.TelegramBotToken == "" {
		missing = append(missing, "PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN")
	}
	if cfg.TelegramChatID == "" {
		missing = append(missing, "PROCESS_ANALYZE_TELEGRAM_CHAT_ID")
	}
	return cfg, missing
}

func (c Config) NewProcessor(client *http.Client) *Processor {
	gammaSvc := gamma.NewService(client)
	gammaSvc.BaseURL = c.GammaAPIBaseURL

	openrouterSvc := openrouter.NewService(c.OpenRouterAuthorization, c.OpenRouterModel, client)
	openrouterSvc.BaseURL = c.OpenRouterAPIBaseURL
	openrouterSvc.Polymarket = gammaSvc

	telegramSvc := telegrambot.NewService(c.TelegramBotToken, c.TelegramChatID, client)
	telegramSvc.BaseURL = c.TelegramAPIBaseURL

	return NewProcessor(c.NewStore(), openrouterSvc, telegramSvc)
}

func (c Config) NewOpenRouter(client *http.Client) *openrouter.Service {
	svc := openrouter.NewService(c.OpenRouterAuthorization, c.OpenRouterModel, client)
	svc.BaseURL = c.OpenRouterAPIBaseURL
	return svc
}

func (c Config) NewGamma(client *http.Client) *gamma.Service {
	svc := gamma.NewService(client)
	svc.BaseURL = c.GammaAPIBaseURL
	return svc
}

func (c Config) NewStore() *processdb.Service {
	return processdb.NewService(c.SQLitePath, c.SQLiteTable)
}

func (c Config) NewTelegram(client *http.Client) *telegrambot.Service {
	svc := telegrambot.NewService(c.TelegramBotToken, c.TelegramChatID, client)
	svc.BaseURL = c.TelegramAPIBaseURL
	return svc
}

func env(getenv func(string) string, key, fallback string) string {
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func openrouterBaseURL() string { return "https://openrouter.ai/api/v1" }
func gammaBaseURL() string      { return "https://gamma-api.polymarket.com" }
func telegramBaseURL() string   { return "https://api.telegram.org" }
