package store

import (
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// storedGeoObjects holds the stored geo objects an import may match.
type storedGeoObjects struct {
	byExternalID map[string]*GeoObject
	byName       map[geoObjectNameKey]*GeoObject
}

// match returns the stored geo object g is, if any: the one with g's
// external ID, else the one with g's group and name.
func (s storedGeoObjects) match(g ExportGeoObject) *GeoObject {
	if g.ExternalID != nil {
		if row := s.byExternalID[*g.ExternalID]; row != nil {
			return row
		}
	}

	return s.byName[geoObjectNameKey{g.Group, g.Name}]
}

func (imp *importer) importGeoObjects(in []ExportStreet) error {
	stored, err := imp.storedGeoObjects(in)
	if err != nil {
		return err
	}

	matched := map[uuid.UUID]bool{}

	var created []GeoObject

	for i, s := range in {
		for j, g := range s.GeoObjects {
			row := GeoObject{
				Name: g.Name, Group: g.Group, StreetID: imp.streetIDs[i], Latitude: g.Latitude, Longitude: g.Longitude,
				ExternalID: g.ExternalID,
			}
			if g.HouseNumber != nil {
				id := imp.houseNumberIDs[row.StreetID][g.HouseNumber.key()]
				row.HouseNumberID = &id
			}

			old := stored.match(g)
			if old == nil {
				created = append(created, row)
				continue
			}

			if matched[old.ID] {
				return fmt.Errorf("streets[%d].geoObjects[%d]: %w", i, j, ErrGeoObjectMatchedTwice)
			}

			matched[old.ID] = true

			if err := imp.apply(&imp.result.GeoObjects, &GeoObject{}, old.ID, old.CollectorID, geoObjectChanges(*old, row)); err != nil {
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

	imp.result.GeoObjects.Created = len(created)

	return nil
}

// geoObjectChanges returns the columns to set to turn old into g. A nil
// external ID keeps the stored one, as GeoObjectStore.Update does.
func geoObjectChanges(old, g GeoObject) map[string]any {
	changes := coordinateChanges(old.Latitude, old.Longitude, g.Latitude, g.Longitude)

	if old.Name != g.Name {
		changes["name"] = g.Name
	}

	if old.Group != g.Group {
		changes["group_name"] = g.Group
	}

	if old.StreetID != g.StreetID {
		changes["street_id"] = g.StreetID
	}

	if !equalPtr(old.HouseNumberID, g.HouseNumberID) {
		changes["house_number_id"] = nil
		if g.HouseNumberID != nil {
			changes["house_number_id"] = *g.HouseNumberID
		}
	}

	if g.ExternalID != nil && !equalPtr(old.ExternalID, g.ExternalID) {
		changes["external_id"] = *g.ExternalID
	}

	return changes
}

// equalPtr reports whether a and b are both nil or point to equal values.
func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}

// storedGeoObjects looks up the stored geo objects with the external IDs and
// the group and name pairs of the imported ones.
func (imp *importer) storedGeoObjects(in []ExportStreet) (storedGeoObjects, error) {
	var (
		externalIDs []string
		names       [][]any
	)

	for _, s := range in {
		for _, g := range s.GeoObjects {
			if g.ExternalID != nil {
				externalIDs = append(externalIDs, *g.ExternalID)
			}

			names = append(names, []any{g.Group, g.Name})
		}
	}

	stored := storedGeoObjects{byExternalID: map[string]*GeoObject{}, byName: map[geoObjectNameKey]*GeoObject{}}
	byID := map[uuid.UUID]*GeoObject{}

	add := func(rows []GeoObject) {
		for _, r := range rows {
			row := byID[r.ID]
			if row == nil {
				row = &r
				byID[r.ID] = row
			}

			if row.ExternalID != nil {
				stored.byExternalID[*row.ExternalID] = row
			}

			stored.byName[geoObjectNameKey{row.Group, row.Name}] = row
		}
	}

	for chunk := range slices.Chunk(externalIDs, importLookupBatchSize) {
		var rows []GeoObject
		if err := imp.locked().Where("external_id IN ?", chunk).Find(&rows).Error; err != nil {
			return stored, err
		}

		add(rows)
	}

	for chunk := range slices.Chunk(names, importLookupBatchSize) {
		var rows []GeoObject
		if err := imp.locked().Where("(group_name, name) IN ?", chunk).Find(&rows).Error; err != nil {
			return stored, err
		}

		add(rows)
	}

	return stored, nil
}
