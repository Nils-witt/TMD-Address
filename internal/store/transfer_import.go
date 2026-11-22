package store

import (
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// importLookupBatchSize caps the keys per lookup of stored rows, keeping each
// statement well below PostgreSQL's limit of 65535 bind parameters.
const importLookupBatchSize = 1000

// ErrGeoObjectMatchedTwice is returned when two geo objects of an import
// match the same stored one, one by external ID and the other by name.
var ErrGeoObjectMatchedTwice = errors.New("matches a stored geo object another geo object of the import matches too")

// importer is the state of one Import, inside its transaction.
type importer struct {
	tx     *gorm.DB
	mode   ImportMode
	result ImportResult

	// streetIDs holds the stored ID of each street of the document, by index.
	streetIDs []uuid.UUID
	// houseNumberIDs holds the stored ID of each house number of the
	// document, by street ID and key.
	houseNumberIDs map[uuid.UUID]map[houseNumberKey]uuid.UUID
}

// run imports streets first, then house numbers, then geo objects, so each
// kind of row can refer to the stored IDs of the ones before.
func (imp *importer) run(doc Export) error {
	if err := imp.importStreets(doc.Streets); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			// A street of the import was created concurrently.
			err = ErrStreetAlreadyExists
		}

		return fmt.Errorf("streets: %w", err)
	}

	if err := imp.importHouseNumbers(doc.Streets); err != nil {
		return fmt.Errorf("houseNumbers: %w", houseNumberWriteError(err))
	}

	if err := imp.importGeoObjects(doc.Streets); err != nil {
		return fmt.Errorf("geoObjects: %w", geoObjectWriteError(err))
	}

	return nil
}

// apply counts a matched row and, if it differs and may be changed, writes
// changes to it.
func (imp *importer) apply(stats *ImportEntityStats, model any, id uuid.UUID, owner *string, changes map[string]any) error {
	switch {
	case len(changes) == 0:
		stats.Unchanged++
		return nil
	case owner != nil || imp.mode != ImportUpdate:
		stats.Skipped++
		return nil
	}

	stats.Updated++

	return imp.tx.Model(model).Where("id = ?", id).Updates(changes).Error
}

// locked returns a query that locks the rows it reads until the import ends,
// so a collector run can't take or change them meanwhile.
func (imp *importer) locked() *gorm.DB {
	return imp.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
}

func (imp *importer) importStreets(in []ExportStreet) error {
	stored, err := imp.storedStreets(in)
	if err != nil {
		return err
	}

	imp.streetIDs = make([]uuid.UUID, len(in))

	var (
		created    []Street
		createdIdx []int
	)

	for i, s := range in {
		row, ok := stored[streetKey{s.Name, s.District, s.City, s.Country}]
		if !ok {
			created = append(created, Street{
				Name: s.Name, District: s.District, City: s.City, Country: s.Country, Latitude: s.Latitude, Longitude: s.Longitude,
			})
			createdIdx = append(createdIdx, i)

			continue
		}

		imp.streetIDs[i] = row.ID

		changes := coordinateChanges(row.Latitude, row.Longitude, s.Latitude, s.Longitude)
		if err := imp.apply(&imp.result.Streets, &Street{}, row.ID, row.CollectorID, changes); err != nil {
			return err
		}
	}

	if len(created) == 0 {
		return nil
	}

	if err := imp.tx.CreateInBatches(&created, insertBatchSize).Error; err != nil {
		return err
	}

	for j, i := range createdIdx {
		imp.streetIDs[i] = created[j].ID
	}

	imp.result.Streets.Created = len(created)

	return nil
}

func (imp *importer) storedStreets(in []ExportStreet) (map[streetKey]Street, error) {
	stored := make(map[streetKey]Street, len(in))

	for chunk := range slices.Chunk(in, importLookupBatchSize) {
		keys := make([][]any, len(chunk))
		for i, s := range chunk {
			keys[i] = []any{s.Name, s.District, s.City, s.Country}
		}

		var rows []Street
		if err := imp.locked().Where("(name, district, city, country) IN ?", keys).Find(&rows).Error; err != nil {
			return nil, err
		}

		for _, r := range rows {
			stored[streetKey{r.Name, r.District, r.City, r.Country}] = r
		}
	}

	return stored, nil
}

func (imp *importer) importHouseNumbers(in []ExportStreet) error {
	stored, err := imp.storedHouseNumbers()
	if err != nil {
		return err
	}

	imp.houseNumberIDs = make(map[uuid.UUID]map[houseNumberKey]uuid.UUID, len(in))

	var created []HouseNumber

	for i, s := range in {
		streetID := imp.streetIDs[i]

		for _, h := range s.HouseNumbers {
			row, ok := stored[streetID][h.key()]
			if !ok {
				created = append(created, HouseNumber{
					StreetID: streetID, Number: h.Number, NumberAddition: h.NumberAddition, Postcode: h.Postcode,
					Latitude: h.Latitude, Longitude: h.Longitude,
				})

				continue
			}

			imp.houseNumberID(streetID, h.key(), row.ID)

			changes := coordinateChanges(row.Latitude, row.Longitude, h.Latitude, h.Longitude)
			if row.Postcode != h.Postcode {
				changes["postcode"] = h.Postcode
			}

			if err := imp.apply(&imp.result.HouseNumbers, &HouseNumber{}, row.ID, row.CollectorID, changes); err != nil {
				return err
			}
		}
	}

	if len(created) == 0 {
		return nil
	}

	if err := imp.tx.CreateInBatches(&created, insertBatchSize).Error; err != nil {
		return err
	}

	for _, h := range created {
		imp.houseNumberID(h.StreetID, newHouseNumberKey(h.Number, h.NumberAddition), h.ID)
	}

	imp.result.HouseNumbers.Created = len(created)

	return nil
}

// storedHouseNumbers returns the stored house numbers of the imported
// streets, by street ID and key.
func (imp *importer) storedHouseNumbers() (map[uuid.UUID]map[houseNumberKey]HouseNumber, error) {
	stored := make(map[uuid.UUID]map[houseNumberKey]HouseNumber, len(imp.streetIDs))

	for chunk := range slices.Chunk(imp.streetIDs, importLookupBatchSize) {
		var rows []HouseNumber
		if err := imp.locked().Where("street_id IN ?", chunk).Find(&rows).Error; err != nil {
			return nil, err
		}

		for _, r := range rows {
			byKey := stored[r.StreetID]
			if byKey == nil {
				byKey = map[houseNumberKey]HouseNumber{}
				stored[r.StreetID] = byKey
			}

			byKey[newHouseNumberKey(r.Number, r.NumberAddition)] = r
		}
	}

	return stored, nil
}

func (imp *importer) houseNumberID(streetID uuid.UUID, key houseNumberKey, id uuid.UUID) {
	ids := imp.houseNumberIDs[streetID]
	if ids == nil {
		ids = map[houseNumberKey]uuid.UUID{}
		imp.houseNumberIDs[streetID] = ids
	}

	ids[key] = id
}

// coordinateChanges returns the columns to set to move a row from
// (lat, lon) to (newLat, newLon).
func coordinateChanges(lat, lon, newLat, newLon float64) map[string]any {
	changes := map[string]any{}

	if lat != newLat {
		changes["latitude"] = newLat
	}

	if lon != newLon {
		changes["longitude"] = newLon
	}

	return changes
}
