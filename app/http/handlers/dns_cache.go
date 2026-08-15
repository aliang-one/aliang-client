package handlers

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strconv"

	"aliang.one/nursorgate/processor/cache"
	"aliang.one/nursorgate/processor/rules"
)

// DNSCacheHandler handles DNS cache management API endpoints
type DNSCacheHandler struct {
	// cache is obtained dynamically via getCache() method
}

// NewDNSCacheHandler creates a new DNS cache handler
func NewDNSCacheHandler() *DNSCacheHandler {
	handler := &DNSCacheHandler{}
	// Get cache from rules engine when needed
	return handler
}

// getCache returns the IP domain cache from the rules engine
func (h *DNSCacheHandler) getCache() *cache.IPDomainCache {
	return rules.GetCache()
}

// GetCacheEntries returns all cache entries with pagination
func (h *DNSCacheHandler) GetCacheEntries(w http.ResponseWriter, r *http.Request) {
	pageStr := r.URL.Query().Get("page")
	limitStr := r.URL.Query().Get("limit")
	sortBy := r.URL.Query().Get("sortBy")

	page := 1
	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	}

	limit := 100
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 1000 {
		limit = l
	}

	cache := h.getCache()
	if cache == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "DNS cache is not initialized"})
		return
	}

	entries := cache.GetAll()

	// Sort entries
	h.sortEntries(entries, sortBy)

	// Paginate
	offset := (page - 1) * limit
	if offset >= len(entries) {
		offset = 0
		page = 1
	}

	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}

	paginatedEntries := entries[offset:end]

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": paginatedEntries,
		"total":   len(entries),
		"page":    page,
		"limit":   limit,
	})
}

// GetStatistics returns cache statistics
func (h *DNSCacheHandler) GetStatistics(w http.ResponseWriter, r *http.Request) {
	cache := h.getCache()
	if cache == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "DNS cache is not initialized"})
		return
	}

	stats := cache.Stats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// GetHotspots returns hot domains and IPs
func (h *DNSCacheHandler) GetHotspots(w http.ResponseWriter, r *http.Request) {
	cache := h.getCache()
	if cache == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "DNS cache is not initialized"})
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
		limit = l
	}

	entries := cache.GetAll()

	// Find hotspot domains (top by hit count)
	type DomainStat struct {
		Domain   string   `json:"domain"`
		HitCount uint64   `json:"hitCount"`
		IP       string   `json:"ip"`
		Sources  []string `json:"sources"`
	}

	domainStats := make(map[string]*DomainStat)
	for _, entry := range entries {
		if _, ok := domainStats[entry.Domain]; !ok {
			sources := make([]string, len(entry.BindingSources))
			for i, src := range entry.BindingSources {
				sources[i] = string(src)
			}
			domainStats[entry.Domain] = &DomainStat{
				Domain:  entry.Domain,
				IP:      entry.IP.String(),
				Sources: sources,
			}
		}
		domainStats[entry.Domain].HitCount += entry.HitCount
	}

	// Convert to slice and sort
	var domainList []*DomainStat
	for _, stat := range domainStats {
		domainList = append(domainList, stat)
	}

	// Sort by hit count descending
	for i := 0; i < len(domainList)-1; i++ {
		for j := i + 1; j < len(domainList); j++ {
			if domainList[j].HitCount > domainList[i].HitCount {
				domainList[i], domainList[j] = domainList[j], domainList[i]
			}
		}
	}

	if len(domainList) > limit {
		domainList = domainList[:limit]
	}

	// Get hotspot IPs
	ipList := cache.GetHotspotIPs(limit)

	type IPStat struct {
		IP                string   `json:"ip"`
		HitCount          uint64   `json:"hitCount"`
		AssociatedDomains []string `json:"associatedDomains"`
		SourceCount       int      `json:"sourceCount"`
	}

	var ipStats []*IPStat
	for _, stat := range ipList {
		ipStats = append(ipStats, &IPStat{
			IP:                stat.IP.String(),
			HitCount:          stat.HitCount,
			AssociatedDomains: stat.AssociatedDomains,
			SourceCount:       stat.SourceCount,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"topDomains": domainList,
		"topIPs":     ipStats,
	})
}

// QueryDomain queries a specific domain
func (h *DNSCacheHandler) QueryDomain(w http.ResponseWriter, r *http.Request) {
	cache := h.getCache()
	if cache == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "DNS cache is not initialized"})
		return
	}

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "domain parameter required"})
		return
	}

	entry, found := cache.GetByDomain(domain)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "domain not found in cache"})
		return
	}

	sources := make([]string, len(entry.BindingSources))
	for i, src := range entry.BindingSources {
		sources[i] = string(src)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"domain":    entry.Domain,
		"ip":        entry.IP.String(),
		"route":     entry.Route,
		"sources":   sources,
		"hitCount":  entry.HitCount,
		"createdAt": entry.CreatedAt,
		"expiresAt": entry.ExpiresAt,
	})
}

// ReverseQuery performs IP reverse query
func (h *DNSCacheHandler) ReverseQuery(w http.ResponseWriter, r *http.Request) {
	cache := h.getCache()
	if cache == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "DNS cache is not initialized"})
		return
	}

	ipStr := r.URL.Query().Get("ip")
	if ipStr == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "ip parameter required"})
		return
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid IP address"})
		return
	}

	// Convert to netip.Addr
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to parse IP address"})
		return
	}

	entries := cache.GetByIP(addr)
	stats := cache.GetIPStatistics(addr)

	type DomainInfo struct {
		Domain   string   `json:"domain"`
		Route    string   `json:"route"`
		Sources  []string `json:"sources"`
		HitCount uint64   `json:"hitCount"`
	}

	var domains []*DomainInfo
	for _, entry := range entries {
		sources := make([]string, len(entry.BindingSources))
		for i, src := range entry.BindingSources {
			sources[i] = string(src)
		}
		domains = append(domains, &DomainInfo{
			Domain:   entry.Domain,
			Route:    string(entry.Route),
			Sources:  sources,
			HitCount: entry.HitCount,
		})
	}

	var result map[string]interface{}
	if stats != nil {
		result = map[string]interface{}{
			"ip":                ipStr,
			"domains":           domains,
			"hitCount":          stats.HitCount,
			"associatedDomains": stats.AssociatedDomains,
			"sourceCount":       stats.SourceCount,
			"isHotspot":         stats.IsHotspot,
			"firstSeen":         stats.FirstSeen,
			"lastSeen":          stats.LastSeen,
		}
	} else {
		result = map[string]interface{}{
			"ip":      ipStr,
			"domains": domains,
			"message": "IP not found or has expired entries",
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// DeleteEntry deletes a cache entry by domain
func (h *DNSCacheHandler) DeleteEntry(w http.ResponseWriter, r *http.Request) {
	cache := h.getCache()
	if cache == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "DNS cache is not initialized"})
		return
	}

	domain := r.PathValue("domain")
	if domain == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "domain required"})
		return
	}

	cache.Delete(domain)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "deleted",
		"domain": domain,
	})
}

// ClearAll clears all cache entries
func (h *DNSCacheHandler) ClearAll(w http.ResponseWriter, r *http.Request) {
	cache := h.getCache()
	if cache == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "DNS cache is not initialized"})
		return
	}

	cache.Clear()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "cleared",
	})
}

// Helper functions
func (h *DNSCacheHandler) sortEntries(entries []*cache.CacheEntry, sortBy string) {
	if sortBy == "hits" {
		// Sort by hit count descending
		for i := 0; i < len(entries)-1; i++ {
			for j := i + 1; j < len(entries); j++ {
				if entries[j].HitCount > entries[i].HitCount {
					entries[i], entries[j] = entries[j], entries[i]
				}
			}
		}
	} else if sortBy == "recent" {
		// Sort by created time descending
		for i := 0; i < len(entries)-1; i++ {
			for j := i + 1; j < len(entries); j++ {
				if entries[j].CreatedAt.After(entries[i].CreatedAt) {
					entries[i], entries[j] = entries[j], entries[i]
				}
			}
		}
	}
	// Default: sort by domain name
}
