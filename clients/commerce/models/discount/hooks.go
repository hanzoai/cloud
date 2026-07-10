package discount

// Invalidate cache based on scope
func (d *Discount) AfterCreate() error {
	return d.invalidateCache()
}

func (d *Discount) AfterUpdate(previous *Discount) error {
	return d.invalidateCache()
}

func (d *Discount) AfterDelete() error {
	return d.invalidateCache()
}
