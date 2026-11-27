package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/sfathall/aide/config"
	"github.com/sfathall/aide/tasks"
)

type configReloader interface {
	Reload(cfg *config.Config)
}

type tasksReloader interface {
	reload(enabled []tasks.Task)
}

// watchFiles polls configPath and tasksPath every interval for mtime changes.
// On config change it calls Reload on every reloader; on tasks change it
// calls tr.reload. Changes that fail to parse are logged and the previous
// config is kept.
func watchFiles(ctx context.Context, configPath, tasksPath string, tr tasksReloader, interval time.Duration, reloaders ...configReloader) {
	log := slog.With("component", "watcher")

	configMtime := fileMtime(configPath)
	tasksMtime := fileMtime(tasksPath)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if mt := fileMtime(configPath); !mt.Equal(configMtime) {
				configMtime = mt
				log.Info("config file changed, reloading", "path", configPath)
				cfg, err := config.Load(configPath)
				if err != nil {
					log.Error("config reload failed, keeping previous config", "err", err)
				} else {
					cfg.ExpandPaths()
					for _, r := range reloaders {
						r.Reload(cfg)
					}
				}
			}
			if mt := fileMtime(tasksPath); !mt.Equal(tasksMtime) {
				tasksMtime = mt
				log.Info("tasks file changed, reloading", "path", tasksPath)
				tc, err := tasks.Load(tasksPath)
				if err != nil {
					log.Error("tasks reload failed, keeping previous tasks", "err", err)
				} else {
					tr.reload(tc.EnabledTasks())
				}
			}
		}
	}
}

func fileMtime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
