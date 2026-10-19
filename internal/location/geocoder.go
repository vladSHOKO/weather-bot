package location

import "context"

type Geocoder interface {
	Search(context.Context, string) (Location, error)
}
type CandidateGeocoder interface {
	Candidates(context.Context, string) ([]Location, error)
}
