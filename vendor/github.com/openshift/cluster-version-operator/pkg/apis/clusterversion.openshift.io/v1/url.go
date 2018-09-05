package v1

import (
	"encoding/json"
	"net/url"
)

// UnmarshalJSON unmarshals a URL, ensuring that it is valid.
func (u *URL) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if _, err := url.Parse(raw); err != nil {
		return err
	}

	*u = URL(raw)

	return nil
}
