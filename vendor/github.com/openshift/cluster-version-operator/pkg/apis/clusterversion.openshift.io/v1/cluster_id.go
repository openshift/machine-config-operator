package v1

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// UnmarshalJSON unmarshals RFC4122 uuid from string.
func (cid *ClusterID) UnmarshalJSON(b []byte) error {
	var strid string
	if err := json.Unmarshal(b, &strid); err != nil {
		return err
	}

	uid, err := uuid.Parse(strid)
	if err != nil {
		return err
	}
	if uid.Variant() != uuid.RFC4122 {
		return fmt.Errorf("invalid ClusterID %q, must be an RFC4122-variant UUID: found %s", strid, uid.Variant())
	}
	if uid.Version() != 4 {
		return fmt.Errorf("Invalid ClusterID %q, must be a version-4 UUID: found %s", strid, uid.Version())
	}

	*cid = ClusterID(uid.String())
	return nil
}
