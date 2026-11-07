// Команда api-gateway — Control Plane платформы.
//
// Режимы:
//   - api-gateway — запускает HTTP-сервер (по умолчанию);
//   - api-gateway bootstrap-admin <email> — создать администратора и напечатать ключ один раз;
//   - api-gateway set-password <email> [password] — задать пароль входа в панель
//     (без аргумента пароль читается из stdin, чтобы не остаться в истории команд);
//   - api-gateway issue-key <email> [scopes,csv] [name] — выпустить дополнительный ключ;
//   - api-gateway revoke-key <key-uuid> — отозвать ключ;
//   - api-gateway version — напечатать версию.
//
// Окружение: LISTEN_ADDR, DATABASE_URL, NATS_URL, API_PEPPER.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/private-compute/network/internal/auth"
	"github.com/private-compute/network/internal/httpapi"
	"github.com/private-compute/network/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// version переопределяется при сборке: -X main.version=0.4.0.
// Это переменная, а не константа: линкер подменяет только переменные.
var version = "0.4.0"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}

	switch command {
	case "serve":
		runServe(logger)
	case "bootstrap-admin":
		if len(args) < 1 {
			fail("usage: api-gateway bootstrap-admin <email>")
		}
		runBootstrapAdmin(logger, args[0])
	case "set-password":
		if len(args) < 1 {
			fail("usage: api-gateway set-password <email> [password]")
		}
		password := ""
		if len(args) > 1 {
			password = args[1]
		}
		runSetPassword(logger, args[0], password)
	case "issue-key":
		if len(args) < 1 {
			fail("usage: api-gateway issue-key <email> [scopes,csv] [name]")
		}
		scopes := []string{"models:read", "usage:read", "chat:write"}
		name := "manual"
		if len(args) > 1 && strings.TrimSpace(args[1]) != "" {
			scopes = splitCSV(args[1])
		}
		if len(args) > 2 {
			name = args[2]
		}
		runIssueKey(logger, args[0], scopes, name)
	case "revoke-key":
		if len(args) < 1 {
			fail("usage: api-gateway revoke-key <key-uuid>")
		}
		runRevokeKey(logger, args[0])
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		fail("unknown command: " + command)
	}
}

func runServe(logger *slog.Logger) {
	pepper := mustPepper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := store.New(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		fail("database: " + err.Error())
	}
	defer db.Close()

	cfg := httpapi.Config{
		Addr:      env("LISTEN_ADDR", "127.0.0.1:8080"),
		Version:   version,
		Pepper:    pepper,
		NatsURL:   os.Getenv("NATS_URL"),
		Store:     db,
		Log:       logger,
		StartedAt: time.Now(),
		// Ограничение частоты: защита от того, что один ключ займёт все узлы.
		RateLimiter:   httpapi.NewRateLimiter(envInt("CHAT_RATE_PER_MINUTE", 60), envInt("CHAT_RATE_BURST", 10)),
		EnrollLimiter: httpapi.NewRateLimiter(envInt("ENROLL_RATE_PER_MINUTE", 10), 3),
		ControlURL:    env("PUBLIC_URL", "https://api.nshno.tech"),
		JoinCode:         os.Getenv("JOIN_CODE"),
		AllowOpenEnroll:  os.Getenv("ALLOW_OPEN_ENROLL") == "1",
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(cfg),
		ReadHeaderTimeout: 10 * time.Second,
		// Таймауты на чтение и запись тела не задаются сознательно: сервер
		// обслуживает два долгих потока — ответ модели (SSE) и приём чанков от
		// узла (NDJSON в теле запроса). С ReadTimeout 30 s поток чанков рвался
		// ровно через полминуты, и ответ обрывался на середине. Ограничения
		// задаются на уровне обработчиков: pullTimeout, chatTimeout и контекст
		// запроса.
		ReadTimeout:    0,
		WriteTimeout:   0,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		logger.Info("api-gateway starting", "addr", cfg.Addr, "version", version)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
	logger.Info("api-gateway stopped")
}

func runBootstrapAdmin(logger *slog.Logger, email string) {
	ctx := context.Background()
	pepper := mustPepper()
	db := mustStore(ctx)
	defer db.Close()

	user, err := db.EnsureUser(ctx, email)
	if err != nil {
		fail("create user: " + err.Error())
	}

	scopes := []string{"admin", "models:read", "chat:write", "usage:read"}
	token, keyID, err := createKey(ctx, db, pepper, user.ID, "bootstrap-admin", scopes)
	if err != nil {
		fail("create key: " + err.Error())
	}

	logger.Info("admin created", "email", user.Email, "user_id", user.ID)
	fmt.Println("user_id: " + user.ID)
	fmt.Println("email:   " + user.Email)
	fmt.Println("scopes:  " + strings.Join(scopes, ","))
	fmt.Println("token:   " + token.String())
	fmt.Println("key_id:  " + keyID)
	fmt.Println()
	fmt.Println("The secret is shown once. Store it in a secret manager.")
}

// runSetPassword задаёт пароль входа в панель.
//
// Пароль не принимается аргументом по умолчанию: аргументы остаются в истории
// оболочки. Если он не передан, читаем строку из stdin.
func runSetPassword(_ *slog.Logger, email, password string) {
	ctx := context.Background()
	db := mustStore(ctx)
	defer db.Close()

	account, err := db.AccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail("account not found: " + email)
		}
		fail("find account: " + err.Error())
	}

	if password == "" {
		fmt.Print("new password: ")
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			fail("cannot read password: " + err.Error())
		}
		password = strings.TrimRight(line, "\r\n")
	}
	if len([]rune(password)) < 10 {
		fail("password must be at least 10 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		fail("hash password: " + err.Error())
	}
	if err := db.SetPassword(ctx, account.ID, string(hash)); err != nil {
		fail("set password: " + err.Error())
	}
	// Пароль сменили — все прежние входы в панель закрыты.
	fmt.Println("password updated for " + account.Email)
	fmt.Println("active sessions revoked: all")
}

func runIssueKey(logger *slog.Logger, email string, scopes []string, name string) {
	ctx := context.Background()
	pepper := mustPepper()
	db := mustStore(ctx)
	defer db.Close()

	user, err := db.UserByEmail(ctx, email)
	if err != nil {
		fail("find user: " + err.Error())
	}

	token, keyID, err := createKey(ctx, db, pepper, user.ID, name, scopes)
	if err != nil {
		fail("create key: " + err.Error())
	}

	logger.Info("key issued", "email", user.Email, "key_id", keyID, "scopes", strings.Join(scopes, ","))
	fmt.Println("user_id: " + user.ID)
	fmt.Println("scopes:  " + strings.Join(scopes, ","))
	fmt.Println("token:   " + token.String())
	fmt.Println("key_id:  " + keyID)
}

func runRevokeKey(logger *slog.Logger, keyUUID string) {
	ctx := context.Background()
	db := mustStore(ctx)
	defer db.Close()

	if err := db.RevokeKey(ctx, keyUUID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail("key not found or already revoked: " + keyUUID)
		}
		fail("revoke: " + err.Error())
	}
	logger.Info("key revoked", "key", keyUUID)
	fmt.Println("revoked: " + keyUUID)
}

func createKey(ctx context.Context, db *store.Store, pepper []byte, userID, name string, scopes []string) (auth.Token, string, error) {
	token, err := auth.NewToken()
	if err != nil {
		return auth.Token{}, "", err
	}
	key, err := db.CreateKey(ctx, userID, token.KeyID, name, auth.Hash(pepper, token.Secret), scopes)
	if err != nil {
		return auth.Token{}, "", err
	}
	return token, key.ID, nil
}

func mustStore(ctx context.Context) *store.Store {
	db, err := store.New(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		fail("database: " + err.Error())
	}
	return db
}

func mustPepper() []byte {
	value := os.Getenv("API_PEPPER")
	if len(value) < 32 {
		fail("API_PEPPER must be set and at least 32 characters long")
	}
	return []byte(value)
}

func mustEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		fail("environment variable is required: " + key)
	}
	return value
}

// envInt читает целое из окружения; пустое или мусорное значение — умолчание.
func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	os.Exit(1)
}
