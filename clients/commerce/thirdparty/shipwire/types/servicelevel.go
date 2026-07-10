package types

type ServiceLevelCode string

const (
	DomesticGround        ServiceLevelCode = "GD"
	Domestic2Day          ServiceLevelCode = "2D"
	Domestic1Day          ServiceLevelCode = "1D"
	InternationalEconomy  ServiceLevelCode = "E-INTL"
	InternationalStandard ServiceLevelCode = "INTL"
	InternationalPlus     ServiceLevelCode = "PL-INTL"
	InternationalPremium  ServiceLevelCode = "PM-INTL"
)
