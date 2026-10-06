package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Cloud é o que o serviço de metadados da Oracle conta sobre a VM (região,
// shape, OCPUs, RAM). Fora da Oracle fica vazio e o painel não mostra os
// limites do Always Free.
type Cloud struct {
	Provider   string  `json:"provider"` // "oracle" ou ""
	Region     string  `json:"region"`   // sa-saopaulo-1
	RegionName string  `json:"regionName"`
	AD         string  `json:"ad"`
	Shape      string  `json:"shape"`
	OCPUs      float64 `json:"ocpus"`
	MemGB      float64 `json:"memGb"`
	NetGbps    float64 `json:"netGbps"`
	Name       string  `json:"name"` // nome da instância no console
}

// Família do shape para os limites grátis.
func (c Cloud) FreeKind() string {
	switch {
	case c.Provider != "oracle":
		return ""
	case strings.Contains(c.Shape, ".A1."):
		return "a1" // Ampere A1: 4 OCPUs + 24 GB no total da conta
	case c.Shape == "VM.Standard.E2.1.Micro":
		return "micro" // AMD Micro: 2 VMs
	}
	return "paid"
}

var regionNames = map[string]string{
	"sa-saopaulo-1": "São Paulo", "sa-vinhedo-1": "Vinhedo", "sa-santiago-1": "Santiago", "sa-valparaiso-1": "Valparaíso",
	"sa-bogota-1": "Bogotá", "us-ashburn-1": "Ashburn", "us-phoenix-1": "Phoenix", "us-sanjose-1": "San Jose",
	"us-chicago-1": "Chicago", "ca-toronto-1": "Toronto", "ca-montreal-1": "Montreal", "mx-queretaro-1": "Querétaro",
	"mx-monterrey-1": "Monterrey", "uk-london-1": "Londres", "eu-frankfurt-1": "Frankfurt", "eu-amsterdam-1": "Amsterdã",
	"eu-madrid-1": "Madri", "eu-paris-1": "Paris", "eu-milan-1": "Milão", "eu-stockholm-1": "Estocolmo",
	"eu-zurich-1": "Zurique", "eu-marseille-1": "Marselha", "ap-tokyo-1": "Tóquio", "ap-osaka-1": "Osaka",
	"ap-seoul-1": "Seul", "ap-singapore-1": "Singapura", "ap-sydney-1": "Sydney", "ap-melbourne-1": "Melbourne",
	"ap-mumbai-1": "Mumbai", "ap-hyderabad-1": "Hyderabad", "me-dubai-1": "Dubai", "me-jeddah-1": "Jeddah",
	"il-jerusalem-1": "Jerusalém", "af-johannesburg-1": "Joanesburgo",
}

// detectOracle pergunta ao serviço de metadados da Oracle (169.254.169.254).
// Responde em milissegundos dentro da Oracle; fora dela, desiste em 3 s.
func detectOracle(ctx context.Context) (Cloud, bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://169.254.169.254/opc/v2/instance/", nil)
	req.Header.Set("Authorization", "Bearer Oracle")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Cloud{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Cloud{}, false
	}
	var d struct {
		DisplayName         string `json:"displayName"`
		CanonicalRegionName string `json:"canonicalRegionName"`
		AvailabilityDomain  string `json:"availabilityDomain"`
		Shape               string `json:"shape"`
		ShapeConfig         struct {
			OCPUs   float64 `json:"ocpus"`
			MemGB   float64 `json:"memoryInGBs"`
			NetGbps float64 `json:"networkingBandwidthInGbps"`
		} `json:"shapeConfig"`
	}
	if json.NewDecoder(resp.Body).Decode(&d) != nil || d.Shape == "" {
		return Cloud{}, false
	}
	c := Cloud{Provider: "oracle", Region: d.CanonicalRegionName, AD: d.AvailabilityDomain, Shape: d.Shape,
		OCPUs: d.ShapeConfig.OCPUs, MemGB: d.ShapeConfig.MemGB, NetGbps: d.ShapeConfig.NetGbps, Name: d.DisplayName}
	c.RegionName = regionNames[c.Region]
	if c.RegionName == "" {
		c.RegionName = c.Region
	}
	return c, true
}
