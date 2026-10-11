package contracts

// ModelSelection is the runtime choice for an agent: which configured provider
// instance, which model, and any reasoning variant/options.
type ModelSelection struct {
	InstanceID string            `json:"instanceId"`
	Model      string            `json:"model"`
	Variant    string            `json:"variant,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
}

// IsZero reports whether no explicit model was chosen (resolution falls back to
// role defaults, then to the instance default).
func (m ModelSelection) IsZero() bool {
	return m.InstanceID == "" && m.Model == "" && m.Variant == ""
}
