package geoip

// CountryInfo contains geolocation information for an IP address
type CountryInfo struct {
	ISOCode string // ISO 3166-1 alpha-2 country code (e.g., "CN", "US")
	Name    string // Country name in English (e.g., "China", "United States")
}
