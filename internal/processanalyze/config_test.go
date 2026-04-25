package processanalyze

import "testing"

func TestLoadConfigFromEnvDefaultsAndMissing(t *testing.T) {
	cfg, missing := LoadConfigFromEnv(func(key string) string {
		switch key {
		case "PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION":
			return "Bearer x"
		case "PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN":
			return "bot"
		case "PROCESS_ANALYZE_TELEGRAM_CHAT_ID":
			return "42"
		default:
			return ""
		}
	})
	if len(missing) != 0 {
		t.Fatalf("expected no missing vars, got %v", missing)
	}
	if cfg.OpenRouterModel != "tencent/hy3-preview:free" || cfg.SQLiteTable != "process_analyze_news" || cfg.SQLitePath != "process-analyze.db" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	_, missing = LoadConfigFromEnv(func(string) string { return "" })
	if len(missing) == 0 {
		t.Fatalf("expected missing vars")
	}
}
