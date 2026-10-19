package weather

type RainRiskLevel string

const (
	RainRiskLow    RainRiskLevel = "low"
	RainRiskMedium RainRiskLevel = "medium"
	RainRiskHigh   RainRiskLevel = "high"
	RainRiskNone   RainRiskLevel = "none"
)

type UmbrellaAdviceLevel string

const (
	UmbrellaNo       UmbrellaAdviceLevel = "no"
	UmbrellaOptional UmbrellaAdviceLevel = "optional"
	UmbrellaYes      UmbrellaAdviceLevel = "yes"
)

type ConfidenceLevel int

const (
	ConfidenceLow ConfidenceLevel = iota + 1
	ConfidenceMedium
	ConfidenceHigh
)

// ForecastDay selects a calendar day in the saved location's timezone.
type ForecastDay int

const (
	Today ForecastDay = iota
	Tomorrow
)
