package openapi

import (
	"reflect"

	"github.com/SigmaUno/subspace/internal/ha"
)

func init() {
	componentNames[reflect.TypeFor[ha.Status]()] = "HAStatus"
	componentNames[reflect.TypeFor[ha.EventStats]()] = "HAEventStats"

	Register(
		get("/api/v1/ha", "Leader election and cross-instance event status of this instance", ha.Status{}),
	)
}
