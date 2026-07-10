package organization

type ByName []*Organization

func (o ByName) Len() int           { return len(o) }
func (o ByName) Swap(i, j int)      { o[i], o[j] = o[j], o[i] }
func (o ByName) Less(i, j int) bool { return o[i].Name < o[j].Name }
