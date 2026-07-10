package bit

type Mask int64

type Field int64

func (f *Field) Set(mask Mask) {
	field := *f
	field |= Field(mask)
	*f = field
}

func (f *Field) Del(mask Mask) {
	field := *f
	field ^= Field(mask)
	*f = field
}

func (f Field) Has(mask Mask) bool {
	return f&Field(mask) != 0
}
