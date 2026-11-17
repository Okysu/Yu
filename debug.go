package yu

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
)

// RouteTopologyDTO represents the topology metadata of a single route for export or rendering.
type RouteTopologyDTO struct {
	RouteKey   string      `json:"route_key"`
	Mode       string      `json:"mode"`
	TotalWaves int         `json:"total_waves"`
	Waves      [][]NodeDTO `json:"waves"`
}

// NodeDTO represents the metadata and input/output type signatures of a single skill node.
type NodeDTO struct {
	Name    string   `json:"name"`
	Inputs  []string `json:"inputs"`
	Outputs []string `json:"outputs"`
}

func (engine *Engine) exportTopologies() []RouteTopologyDTO {
	keys := make([]string, 0, len(engine.router.entries))
	for k := range engine.router.entries {
		// Skip internal debug routes to keep the business view clean.
		if strings.Contains(k, "/debug/dag") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]RouteTopologyDTO, 0, len(keys))
	for _, k := range keys {
		entry := engine.router.entries[k]
		mode := "Lock-Free Parallel"
		if entry.scheduler != nil {
			mode = fmt.Sprintf("Sharded Queue (%d lanes)", len(entry.scheduler.lanes))
		}

		wavesDTO := make([][]NodeDTO, 0, len(entry.dag.Waves))
		for _, wave := range entry.dag.Waves {
			nodesDTO := make([]NodeDTO, 0, len(wave))
			for _, n := range wave {
				ins := make([]string, 0, len(n.InTypes))
				for _, t := range n.InTypes {
					ins = append(ins, t.String())
				}
				outs := make([]string, 0, len(n.OutTypes))
				for _, t := range n.OutTypes {
					outs = append(outs, t.String())
				}
				nodesDTO = append(nodesDTO, NodeDTO{
					Name:    n.Name,
					Inputs:  ins,
					Outputs: outs,
				})
			}
			wavesDTO = append(wavesDTO, nodesDTO)
		}

		result = append(result, RouteTopologyDTO{
			RouteKey:   k,
			Mode:       mode,
			TotalWaves: len(entry.dag.Waves),
			Waves:      wavesDTO,
		})
	}
	return result
}

const debugHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Yu - Runtime Topology Inspector</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0f172a; color: #e2e8f0; margin: 0; padding: 32px; }
  h1 { color: #38bdf8; margin-bottom: 8px; }
  .subtitle { color: #94a3b8; margin-bottom: 32px; font-size: 14px; }
  .route-card { background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 20px; margin-bottom: 24px; }
  .route-header { display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid #334155; padding-bottom: 12px; margin-bottom: 16px; }
  .route-title { font-size: 18px; font-weight: 700; color: #f8fafc; font-family: monospace; }
  .badge { background: #0284c7; color: #fff; padding: 4px 10px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .waves-container { display: flex; gap: 20px; overflow-x: auto; align-items: stretch; }
  .wave-col { background: #0f172a; border: 1px dashed #475569; border-radius: 8px; padding: 12px; min-width: 240px; }
  .wave-title { font-size: 12px; text-transform: uppercase; letter-spacing: 0.05em; color: #38bdf8; margin-bottom: 10px; font-weight: 600; }
  .skill-box { background: #1e293b; border-left: 4px solid #10b981; border-radius: 6px; padding: 10px; margin-bottom: 10px; }
  .skill-name { font-weight: 700; color: #34d399; font-family: monospace; font-size: 14px; margin-bottom: 6px; }
  .io-line { font-size: 12px; color: #cbd5e1; font-family: monospace; margin-top: 3px; }
  .io-tag { color: #94a3b8; }
</style>
</head>
<body>
  <h1>Yu · Deterministic Concurrency Topology</h1>
  <div class="subtitle">Static Startup Compilation · Atomic skills within the same wave execute concurrently across cores without locks</div>
  {{range .}}
  <div class="route-card">
    <div class="route-header">
      <span class="route-title">{{.RouteKey}}</span>
      <span class="badge">{{.Mode}} · {{.TotalWaves}} Wave(s)</span>
    </div>
    <div class="waves-container">
      {{range $idx, $wave := .Waves}}
      <div class="wave-col">
        <div class="wave-title">Wave {{$idx}} ({{len $wave}} parallel)</div>
        {{range $wave}}
        <div class="skill-box">
          <div class="skill-name">{{.Name}}</div>
          <div class="io-line"><span class="io-tag">IN :</span> {{range .Inputs}}[{{.}}] {{end}}</div>
          <div class="io-line"><span class="io-tag">OUT:</span> {{range .Outputs}}[{{.}}] {{end}}</div>
        </div>
        {{end}}
      </div>
      {{end}}
    </div>
  </div>
  {{end}}
</body>
</html>`

// EnableDebugDAG registers the built-in topology visualization endpoint (for example, "/debug/dag").
func (engine *Engine) EnableDebugDAG(path string) {
	tmpl := template.Must(template.New("dag").Parse(debugHTMLTemplate))

	engine.GET(path, func(req Request) Response {
		topologies := engine.exportTopologies()
		if req.QueryDefault("format", "html") == "json" {
			return JSON(200, topologies)
		}

		var sb strings.Builder
		_ = tmpl.Execute(&sb, topologies)
		return Response{
			Status: http.StatusOK,
			Header: map[string]string{"Content-Type": "text/html; charset=utf-8"},
			Body:   []byte(sb.String()),
		}
	})
}
