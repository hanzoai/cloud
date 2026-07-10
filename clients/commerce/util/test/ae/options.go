package ae

// Aliased for simplicity
type Options struct {
	AppID                       string
	Debug                       bool
	StronglyConsistentDatastore bool
	Modules                     []string

	// Deprecated
	Noisy      bool
	TaskQueues []string
}

// Generate safe defaults
func defaults(args ...Options) Options {
	opts := Options{
		AppID: "None",
		Debug: false,
		StronglyConsistentDatastore: true,
		Modules:                     []string{"default"},
	}

	if len(args) == 1 {
		o := (Options)(args[0])
		if o.AppID != "" {
			opts.AppID = o.AppID
		}

		if o.Debug != opts.Debug {
			opts.Debug = o.Debug
		}

		if o.StronglyConsistentDatastore != opts.StronglyConsistentDatastore {
			opts.StronglyConsistentDatastore = o.StronglyConsistentDatastore
		}

		if o.Modules != nil {
			opts.Modules = o.Modules
		}
	}

	return opts
}
