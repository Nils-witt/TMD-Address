package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TransferStore exports streets, house numbers and geo objects into an
// Export document and imports them back, into this or another instance.
type TransferStore struct {
	db *gorm.DB
}

// NewTransferStore returns a TransferStore backed by db.
func NewTransferStore(db *gorm.DB) *TransferStore {
	return &TransferStore{db: db}
}

// Export returns the rows filter selects, as Export.Filter does, ordered by
// country, city, district and name, and within a street by number and by
// group and name.
//
// It reads in one REPEATABLE READ transaction, so a collector run applying
// meanwhile can't leave a geo object pointing at a street the export lacks.
func (ts *TransferStore) Export(ctx context.Context, filter SourceFilter) (Export, error) {
	var (
		streets      []Street
		houseNumbers []HouseNumber
		geoObjects   []GeoObject
	)

	q := filter.queries()

	err := ts.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := q.streets(tx).Order("country, city, district, name").Find(&streets).Error; err != nil {
			return err
		}

		if err := q.houseNumbers(tx).Order("number, coalesce(number_addition, '')").Find(&houseNumbers).Error; err != nil {
			return err
		}

		return q.geoObjects(tx).Order("group_name, name").Find(&geoObjects).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Export{}, fmt.Errorf("read rows: %w", err)
	}

	return buildExport(streets, houseNumbers, geoObjects).Filter(filter), nil
}

// exportQueries narrow the rows Export reads to those a SourceFilter selects
// or that selected rows need. Export.Filter still has the final say; these
// only spare reading all rows for a filtered export.
type exportQueries struct {
	// cond selects the rows the filter selects by their collector_id; it is
	// empty for the zero filter.
	cond string
	args []any
}

func (f SourceFilter) queries() exportQueries {
	var (
		conds []string
		q     exportQueries
	)

	if f.Manual {
		conds = append(conds, "collector_id IS NULL")
	}

	if len(f.Collectors) > 0 {
		conds = append(conds, "collector_id IN ?")
		q.args = append(q.args, f.Collectors)
	}

	if len(conds) > 0 {
		q.cond = "(" + strings.Join(conds, " OR ") + ")"
	}

	return q
}

func (q exportQueries) geoObjects(tx *gorm.DB) *gorm.DB {
	if q.cond == "" {
		return tx
	}

	return tx.Where(q.cond, q.args...)
}

// houseNumberCond selects the house numbers the filter selects and those its
// geo objects are at.
func (q exportQueries) houseNumberCond() (string, []any) {
	return q.cond + " OR id IN (SELECT house_number_id FROM geo_objects WHERE " + q.cond + ")", slices.Concat(q.args, q.args)
}

func (q exportQueries) houseNumbers(tx *gorm.DB) *gorm.DB {
	if q.cond == "" {
		return tx
	}

	cond, args := q.houseNumberCond()

	return tx.Where(cond, args...)
}

// streets selects the streets the filter selects and those of its house
// numbers and geo objects.
func (q exportQueries) streets(tx *gorm.DB) *gorm.DB {
	if q.cond == "" {
		return tx
	}

	hnCond, hnArgs := q.houseNumberCond()
	cond := q.cond +
		" OR id IN (SELECT street_id FROM house_numbers WHERE " + hnCond + ")" +
		" OR id IN (SELECT street_id FROM geo_objects WHERE " + q.cond + ")"

	return tx.Where(cond, slices.Concat(q.args, hnArgs, q.args)...)
}

// buildExport nests houseNumbers and geoObjects under their streets, keeping
// their order.
func buildExport(streets []Street, houseNumbers []HouseNumber, geoObjects []GeoObject) Export {
	out := Export{FormatVersion: ExportFormatVersion, ExportedAt: time.Now().UTC(), Streets: make([]ExportStreet, len(streets))}
	byID := make(map[uuid.UUID]*ExportStreet, len(streets))

	for i, s := range streets {
		out.Streets[i] = ExportStreet{
			Name: s.Name, District: s.District, City: s.City, Country: s.Country,
			Latitude: s.Latitude, Longitude: s.Longitude, CollectorID: s.CollectorID,
			HouseNumbers: []ExportHouseNumber{}, GeoObjects: []ExportGeoObject{},
		}
		byID[s.ID] = &out.Streets[i]
	}

	refs := make(map[uuid.UUID]ExportHouseNumberRef, len(houseNumbers))

	for _, h := range houseNumbers {
		refs[h.ID] = ExportHouseNumberRef{Number: h.Number, NumberAddition: h.NumberAddition}

		if s := byID[h.StreetID]; s != nil {
			s.HouseNumbers = append(s.HouseNumbers, ExportHouseNumber{
				Number: h.Number, NumberAddition: h.NumberAddition, Postcode: h.Postcode,
				Latitude: h.Latitude, Longitude: h.Longitude, CollectorID: h.CollectorID,
			})
		}
	}

	for _, g := range geoObjects {
		s := byID[g.StreetID]
		if s == nil {
			continue
		}

		e := ExportGeoObject{
			Name: g.Name, Group: g.Group, Latitude: g.Latitude, Longitude: g.Longitude,
			ExternalID: g.ExternalID, CollectorID: g.CollectorID,
		}

		if g.HouseNumberID != nil {
			if ref, ok := refs[*g.HouseNumberID]; ok {
				e.HouseNumber = &ref
			}
		}

		s.GeoObjects = append(s.GeoObjects, e)
	}

	return out
}

// ImportMode says what Import does with a row whose key is already stored.
type ImportMode string

// The import modes.
const (
	// ImportSkip leaves stored rows as they are.
	ImportSkip ImportMode = "skip"
	// ImportUpdate overwrites stored manual rows with the imported values.
	// Rows a collector owns are left as they are either way.
	ImportUpdate ImportMode = "update"
)

// ImportOptions control Import.
type ImportOptions struct {
	// Filter selects the rows of the document to import, as Export.Filter
	// does.
	Filter SourceFilter
	// Mode is ImportSkip if empty.
	Mode ImportMode
	// DryRun computes the result without keeping any change.
	DryRun bool
}

// ImportEntityStats counts what an import did to one kind of row.
type ImportEntityStats struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	// Skipped counts rows that were already stored with other values and
	// were left as they are: in ImportSkip mode, or because a collector owns
	// them.
	Skipped int `json:"skipped"`
}

// ImportResult is what Import did.
type ImportResult struct {
	DryRun       bool              `json:"dryRun"`
	Streets      ImportEntityStats `json:"streets"`
	HouseNumbers ImportEntityStats `json:"houseNumbers"`
	GeoObjects   ImportEntityStats `json:"geoObjects"`
}

// errDryRun rolls back the transaction of a dry run.
var errDryRun = errors.New("dry run")

// Import writes the rows of doc that opts.Filter selects, in one transaction:
// either all of them are written, or none is. Rows are matched to stored ones
// by their keys (see Export); new rows are created as manual rows, whoever
// owned them in doc. A geo object's external ID is imported; those of streets
// and house numbers belong to their collectors and are not part of doc.
//
// doc must be valid (see Export.Validate). Import returns ErrNameTaken or
// ErrExternalIDTaken if an imported geo object collides with a stored one it
// wasn't matched to.
func (ts *TransferStore) Import(ctx context.Context, doc Export, opts ImportOptions) (ImportResult, error) {
	if opts.Mode == "" {
		opts.Mode = ImportSkip
	}

	doc = doc.Filter(opts.Filter)

	var result ImportResult

	err := ts.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		imp := &importer{tx: tx, mode: opts.Mode}
		if err := imp.run(doc); err != nil {
			return err
		}

		result = imp.result

		if opts.DryRun {
			return errDryRun
		}

		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}

	if err != nil {
		return ImportResult{}, err
	}

	result.DryRun = opts.DryRun

	return result, nil
}
