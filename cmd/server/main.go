// Command server is the single binary described in docs/SPEC.md §3.
//
// Usage:
//
//	server                                             run the HTTP server
//	server admin create --id ID --name NAME --role ROLE [--dept DEPT]
//	                                                    create an admin account
//
// There is no default account or password (SPEC §11.1): the first admin
// must always be created explicitly via this subcommand.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"emergencycallup/internal/auth"
	"emergencycallup/internal/config"
	"emergencycallup/internal/httpapi"
	"emergencycallup/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		runAdminCommand(os.Args[2:])
		return
	}
	runServer()
}

func runServer() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Printf("configuration invalid, refusing to start:\n%v", err)
		os.Exit(1)
	}

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Printf("database error: %v", err)
		os.Exit(1)
	}
	defer db.Close()

	log.Printf("starting: %s", cfg.Summary())

	srv := httpapi.NewServer(cfg, db)
	srv.BackupHook = func(ctx context.Context) {
		path, err := db.Backup(ctx, cfg.DataDir)
		if err != nil {
			log.Printf("backup failed: %v", err)
			return
		}
		log.Printf("backup written: %s", path)
	}

	// SPEC §6.5: load the active incident's assignments into memory before
	// serving any request.
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := srv.Tracker.LoadActive(bootCtx); err != nil {
		bootCancel()
		log.Printf("failed to load active incident into memory: %v", err)
		os.Exit(1)
	}
	bootCancel()

	stopBackground := make(chan struct{})
	var bg sync.WaitGroup

	// SPEC §6.5: 2-second batch writer for non-transition position updates.
	bg.Add(1)
	go func() {
		defer bg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := srv.Tracker.FlushDirty(context.Background()); err != nil {
					log.Printf("flush error: %v", err)
				}
			case <-stopBackground:
				return
			}
		}
	}()

	// SPEC §12.4: daily 03:00 Asia/Seoul backup, plus §11.4's daily fix-
	// history retention cleanup.
	bg.Add(1)
	go func() {
		defer bg.Done()
		runDailyMaintenance(stopBackground, srv, db, cfg)
	}()

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if cfg.TLSEnabled() {
			errCh <- httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			errCh <- httpServer.ListenAndServe()
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
			close(stopBackground)
			bg.Wait()
			os.Exit(1)
		}
	case <-sigCh:
		log.Printf("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}
	close(stopBackground)
	bg.Wait()
}

func seoulLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		return time.FixedZone("Asia/Seoul", 9*60*60)
	}
	return loc
}

// runDailyMaintenance runs backup + fix-retention cleanup once a day at
// 03:00 Asia/Seoul (SPEC §12.4, §11.4), until stopCh is closed.
func runDailyMaintenance(stopCh <-chan struct{}, srv *httpapi.Server, db *store.DB, cfg *config.Config) {
	for {
		now := time.Now().In(seoulLocation())
		next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, seoulLocation())
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		select {
		case <-time.After(time.Until(next)):
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			srv.BackupHook(ctx)
			cutoff := time.Now().AddDate(0, 0, -cfg.FixRetentionDays).UnixMilli()
			if n, err := srv.Incidents.DeleteOldFixes(ctx, cutoff); err != nil {
				log.Printf("fix retention cleanup failed: %v", err)
			} else if n > 0 {
				log.Printf("fix retention cleanup: removed %d old rows", n)
			}
			cancel()
		case <-stopCh:
			return
		}
	}
}

func runAdminCommand(args []string) {
	if len(args) < 1 || args[0] != "create" {
		fmt.Fprintln(os.Stderr, "usage: server admin create --id <id> --name <name> --role admin|operator [--dept <dept>]")
		os.Exit(2)
	}

	fs := flag.NewFlagSet("admin create", flag.ExitOnError)
	id := fs.String("id", "", "로그인 ID (required)")
	name := fs.String("name", "", "표시 이름 (required)")
	dept := fs.String("dept", "", "소속")
	role := fs.String("role", auth.RoleAdmin, "admin|operator")
	_ = fs.Parse(args[1:])

	if *id == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "--id and --name are required")
		os.Exit(2)
	}
	if *role != auth.RoleAdmin && *role != auth.RoleOperator {
		fmt.Fprintln(os.Stderr, "--role must be admin or operator")
		os.Exit(2)
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration invalid:\n%v\n", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "database error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// DECISION: password is read as a plain line from stdin, not with
	// hidden-echo input. golang.org/x/term (needed for that) is not on the
	// §3.2 allowed dependency list; adding it requires asking first (§0-5).
	// Run this command somewhere your terminal isn't being watched/logged.
	stdin := bufio.NewReader(os.Stdin)
	pw := promptLine(stdin, "새 비밀번호 (8자 이상, 화면에 그대로 표시됩니다): ")
	pw2 := promptLine(stdin, "비밀번호 확인: ")
	if pw != pw2 {
		fmt.Fprintln(os.Stderr, "비밀번호가 일치하지 않습니다.")
		os.Exit(1)
	}

	hasher := auth.NewHasher()
	admins := auth.NewAdminStore(db.DB, hasher)
	admin, err := admins.Create(context.Background(), *id, pw, *name, *dept, *role, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "계정 생성 실패: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("계정을 생성했습니다: id=%s name=%s role=%s\n", admin.LoginID, admin.Name, admin.Role)
}

func promptLine(reader *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	line, _ := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}
