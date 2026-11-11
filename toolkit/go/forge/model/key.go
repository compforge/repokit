// Package model defines shared Forge resource identities.
package model

import "github.com/compforge/quality-harness/sdks/go/common"

type Key struct {
	Repository common.Repository `json:"repository"`
	Number     int               `json:"number"`
}
