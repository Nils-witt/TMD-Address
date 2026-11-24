package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/google/uuid"

	"github.com/nils-witt/tmd-address/internal/collector"
	"github.com/nils-witt/tmd-address/internal/collector/contract"
	"github.com/nils-witt/tmd-address/internal/store"
)

// CollectorConfig wires the collector routes. A nil Runner disables them.
type CollectorConfig struct {
	Runner *collector.Runner
	Store  *store.CollectorStore
	// AdminGroup is the group whose members may manage collectors. Empty
	// lets nobody.
	AdminGroup string
	// Revocations closes the event streams whose token a back-channel logout
	// revoked; nil closes none.
	Revocations TokenRevocations
}

const (
	defaultRunsLimit = 20
	maxRunsLimit     = 100
	// reloadWriteTimeout gives a reload time to describe every collector,
	// beyond the server's default write timeout.
	reloadWriteTimeout = collector.DescribeTimeout + 10*time.Second
)

func registerCollectorRoutes(rg gin.IRouter, cfg CollectorConfig) {
	admin := rg.Group("", requireAdminGroup(cfg.AdminGroup))

	get(admin, "/api/collectors", handleListCollectors(cfg.Store))
	admin.POST("/api/collectors/reload", handleReloadCollectors(cfg))
	get(admin, "/api/collectors/:id", handleGetCollector(cfg.Store))
	admin.PUT("/api/collectors/:id/settings", handleUpdateCollectorSettings(cfg.Store))
	admin.PUT("/api/collectors/:id/schedule", handleUpdateCollectorSchedule(cfg.Store))
	admin.DELETE("/api/collectors/:id", handleDeleteCollector(cfg.Store))
	admin.POST("/api/collectors/:id/runs", handleStartRun(cfg.Runner))
	get(admin, "/api/collectors/:id/runs", handleListRuns(cfg.Store))
	get(admin, "/api/collector-runs/:runId", handleGetRun(cfg.Store))
	admin.POST("/api/collector-runs/:runId/cancel", handleCancelRun(cfg))
	get(admin, "/api/collector-events", handleCollectorEvents(cfg.Runner, cfg.Revocations))
}

// collectorView is a collector as the API shows it: settings without the
// values of secrets, which only say whether they are set.
type collectorView struct {
	store.Collector

	Settings   map[string]json.RawMessage `json:"settings"`
	SecretsSet []string                   `json:"secretsSet"`
	LastRun    *store.CollectorRun        `json:"lastRun,omitempty"`
	// NextRunAt is when the schedule next starts a run.
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`
}

func newCollectorView(c store.Collector, lastRun *store.CollectorRun) collectorView {
	v := collectorView{
		Collector: c, Settings: map[string]json.RawMessage{}, SecretsSet: []string{}, LastRun: lastRun,
	}

	if next, ok := c.NextScheduledRun(); ok {
		v.NextRunAt = &next
	}

	for _, spec := range c.SettingsSchema {
		value := c.Settings[spec.Key]

		switch {
		case value == nil:
		case spec.Type == contract.SettingSecret:
			v.SecretsSet = append(v.SecretsSet, spec.Key)
		default:
			v.Settings[spec.Key] = value
		}
	}

	return v
}

func writeCollectorList(c *gin.Context, cs *store.CollectorStore, collectors []store.Collector) {
	lastRuns, err := cs.LastRuns(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}

	views := make([]collectorView, len(collectors))

	for i, c := range collectors {
		var last *store.CollectorRun
		if run, ok := lastRuns[c.ID]; ok {
			last = &run
		}

		views[i] = newCollectorView(c, last)
	}

	writeJSON(c, http.StatusOK, views)
}

func handleListCollectors(cs *store.CollectorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		collectors, err := cs.List(c.Request.Context())
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeCollectorList(c, cs, collectors)
	}
}

func handleReloadCollectors(cfg CollectorConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(reloadWriteTimeout))

		collectors, err := cfg.Runner.Reload(c.Request.Context())
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeCollectorList(c, cfg.Store, collectors)
	}
}

// writeCollector writes the collector with the given ID, or a 404.
func writeCollector(c *gin.Context, cs *store.CollectorStore, col store.Collector, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, http.StatusNotFound, "collector not found")
		return
	}

	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}

	lastRuns, err := cs.LastRuns(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}

	var last *store.CollectorRun
	if run, ok := lastRuns[col.ID]; ok {
		last = &run
	}

	writeJSON(c, http.StatusOK, newCollectorView(col, last))
}

func handleGetCollector(cs *store.CollectorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		col, err := cs.Get(c.Request.Context(), c.Param("id"))
		writeCollector(c, cs, col, err)
	}
}

func handleUpdateCollectorSettings(cs *store.CollectorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input store.SettingsInput
		if err := json.NewDecoder(c.Request.Body).Decode(&input); err != nil || input == nil {
			writeError(c, http.StatusBadRequest, "body must be a JSON object of settings")
			return
		}

		col, err := cs.UpdateSettings(c.Request.Context(), c.Param("id"), input)
		if errors.Is(err, store.ErrInvalidSettings) {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		writeCollector(c, cs, col, err)
	}
}

// scheduleInput is the body of PUT /api/collectors/{id}/schedule.
type scheduleInput struct {
	Schedule *string `json:"schedule"`
}

func handleUpdateCollectorSchedule(cs *store.CollectorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input scheduleInput
		if err := json.NewDecoder(c.Request.Body).Decode(&input); err != nil || input.Schedule == nil {
			writeError(c, http.StatusBadRequest, `body must be {"schedule": "<cron expression>"}, empty to remove it`)
			return
		}

		col, err := cs.UpdateSchedule(c.Request.Context(), c.Param("id"), *input.Schedule)
		if errors.Is(err, store.ErrInvalidSchedule) {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		writeCollector(c, cs, col, err)
	}
}

func handleDeleteCollector(cs *store.CollectorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		deleteData, err := optionalBool(c, "deleteData")
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		err = cs.Delete(c.Request.Context(), c.Param("id"), deleteData)

		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(c, http.StatusNotFound, "collector not found")
		case errors.Is(err, store.ErrCollectorAvailable), errors.Is(err, store.ErrRunActive):
			writeError(c, http.StatusConflict, err.Error())
		case err != nil:
			writeError(c, http.StatusInternalServerError, err.Error())
		default:
			c.Status(http.StatusNoContent)
		}
	}
}

func handleStartRun(runner *collector.Runner) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, _ := principalFrom(c.Request.Context())

		run, err := runner.Start(c.Request.Context(), c.Param("id"), p.Name)

		var missing *collector.MissingSettingsError

		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(c, http.StatusNotFound, "collector not found")
		case errors.As(err, &missing):
			writeError(c, http.StatusBadRequest, err.Error())
		case errors.Is(err, collector.ErrUnavailable), errors.Is(err, store.ErrRunActive):
			writeError(c, http.StatusConflict, err.Error())
		case err != nil:
			writeError(c, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(c, http.StatusAccepted, run)
		}
	}
}

func handleListRuns(cs *store.CollectorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset, err := pagination(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		id := c.Param("id")
		if _, err := cs.Get(c.Request.Context(), id); errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "collector not found")
			return
		}

		runs, err := cs.ListRuns(c.Request.Context(), id, limit, offset)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, runs)
	}
}

func handleGetRun(cs *store.CollectorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("runId"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid run id")
			return
		}

		run, err := cs.GetRun(c.Request.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "run not found")
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, run)
	}
}

func handleCancelRun(cfg CollectorConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("runId"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid run id")
			return
		}

		if err := cfg.Runner.Cancel(id); errors.Is(err, collector.ErrRunNotActive) {
			if _, err := cfg.Store.GetRun(c.Request.Context(), id); errors.Is(err, store.ErrNotFound) {
				writeError(c, http.StatusNotFound, "run not found")
				return
			}

			writeError(c, http.StatusConflict, err.Error())

			return
		}

		c.Status(http.StatusAccepted)
	}
}

// pagination reads the limit and offset query parameters.
func pagination(c *gin.Context) (limit, offset int, err error) {
	limit, offset = defaultRunsLimit, 0

	if s := c.Query("limit"); s != "" {
		limit, err = strconv.Atoi(s)
		if err != nil || limit < 1 || limit > maxRunsLimit {
			return 0, 0, errors.New("limit must be between 1 and 100")
		}
	}

	if s := c.Query("offset"); s != "" {
		offset, err = strconv.Atoi(s)
		if err != nil || offset < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
	}

	return limit, offset, nil
}

// optionalBool reads a query parameter that is absent, "true" or "false".
func optionalBool(c *gin.Context, key string) (bool, error) {
	s := c.Query(key)
	if s == "" {
		return false, nil
	}

	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, errors.New(key + " must be true or false")
	}

	return b, nil
}
