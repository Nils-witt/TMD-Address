package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nils-witt/tmd-address/internal/store"
)

const (
	// maxImportBytes bounds an import document, which Import holds in memory
	// as a whole.
	maxImportBytes = 128 << 20
	// transferTimeout replaces the server's read and write timeouts for an
	// export or import, which take longer than other requests with a lot of
	// data.
	transferTimeout = 5 * time.Minute
)

func registerTransferRoutes(mux *http.ServeMux, transfer *store.TransferStore) {
	mux.HandleFunc("GET /api/export", handleExport(transfer))
	mux.HandleFunc("POST /api/import", handleImport(transfer))
}

// sourceFilterFromQuery reads the manual and collector parameters shared by
// export and import.
func sourceFilterFromQuery(query url.Values) (store.SourceFilter, error) {
	var filter store.SourceFilter

	if v := query.Get("manual"); v != "" {
		manual, err := strconv.ParseBool(v)
		if err != nil {
			return filter, errors.New("manual must be true or false")
		}

		filter.Manual = manual
	}

	for _, id := range query["collector"] {
		if id == "" {
			return filter, errors.New("collector must not be empty")
		}

		filter.Collectors = append(filter.Collectors, id)
	}

	return filter, nil
}

// extendDeadlines gives a transfer longer than the server's timeouts. The
// error is ignored: a writer that can't change them keeps the server's.
func extendDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(transferTimeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
}

func handleExport(transfer *store.TransferStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := sourceFilterFromQuery(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		extendDeadlines(w)

		doc, err := transfer.Export(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		name := "address-serv-export-" + doc.ExportedAt.Format("20060102-150405") + ".json"
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		writeJSON(w, http.StatusOK, doc)
	}
}

// importOptionsFromQuery reads the import's filter, mode and dryRun
// parameters.
func importOptionsFromQuery(query url.Values) (store.ImportOptions, error) {
	filter, err := sourceFilterFromQuery(query)
	if err != nil {
		return store.ImportOptions{}, err
	}

	opts := store.ImportOptions{Filter: filter, Mode: store.ImportSkip}

	switch mode := store.ImportMode(query.Get("mode")); mode {
	case "", store.ImportSkip:
	case store.ImportUpdate:
		opts.Mode = mode
	default:
		return opts, errors.New("mode must be skip or update")
	}

	if v := query.Get("dryRun"); v != "" {
		if opts.DryRun, err = strconv.ParseBool(v); err != nil {
			return opts, errors.New("dryRun must be true or false")
		}
	}

	return opts, nil
}

func handleImport(transfer *store.TransferStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts, err := importOptionsFromQuery(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		extendDeadlines(w)

		var doc store.Export

		r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
		if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the document must be at most %d bytes", maxImportBytes))
				return
			}

			writeError(w, http.StatusBadRequest, "invalid JSON body")

			return
		}

		if err := doc.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		result, err := transfer.Import(r.Context(), doc, opts)
		if err != nil {
			writeImportError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}

// writeImportError writes the response for an error returned by Import: the
// document conflicts with stored rows, or the import failed.
func writeImportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNameTaken), errors.Is(err, store.ErrExternalIDTaken),
		errors.Is(err, store.ErrStreetAlreadyExists), errors.Is(err, store.ErrHouseNumberAlreadyExists),
		errors.Is(err, store.ErrGeoObjectMatchedTwice):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
