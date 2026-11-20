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
	for k, entry := range engine.router.entries {
		// Skip internal routes and raw handlers: the inspector shows DAGs only.
		if entry.internal || entry.raw != nil {
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
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Yu - Runtime Topology Inspector</title>
<script>
(function () {
  try {
    var saved = localStorage.getItem("yu-debug-theme");
    var theme = saved === "light" || saved === "dark"
      ? saved
      : (window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark");
    document.documentElement.setAttribute("data-theme", theme);
  } catch (e) {}
})();
</script>
<style>
  :root {
    color-scheme: dark;
    --bg: #0f172a;
    --surface: #1e293b;
    --surface-subtle: #0f172a;
    --input-bg: #1e293b;
    --border: #334155;
    --border-dashed: #475569;
    --text: #e2e8f0;
    --text-strong: #f8fafc;
    --text-muted: #94a3b8;
    --text-code: #cbd5e1;
    --accent: #38bdf8;
    --badge-bg: #0284c7;
    --badge-text: #ffffff;
    --skill-border: #10b981;
    --skill-name: #34d399;
    --match-bg: rgba(56, 189, 248, 0.14);
    --match-ring: #38bdf8;
    --shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
  }
  :root[data-theme="light"] {
    color-scheme: light;
    --bg: #f8fafc;
    --surface: #ffffff;
    --surface-subtle: #f1f5f9;
    --input-bg: #ffffff;
    --border: #e2e8f0;
    --border-dashed: #cbd5e1;
    --text: #1e293b;
    --text-strong: #0f172a;
    --text-muted: #64748b;
    --text-code: #334155;
    --accent: #0284c7;
    --badge-bg: #0284c7;
    --badge-text: #ffffff;
    --skill-border: #059669;
    --skill-name: #047857;
    --match-bg: rgba(2, 132, 199, 0.08);
    --match-ring: #0284c7;
    --shadow: 0 1px 3px rgba(15, 23, 42, 0.06);
  }
  * { box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: var(--bg); color: var(--text); margin: 0; padding: 32px; transition: background 0.15s ease, color 0.15s ease; }
  .topbar { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: flex-start; gap: 16px; margin-bottom: 24px; }
  h1 { color: var(--accent); margin: 0 0 8px 0; font-size: 28px; }
  .subtitle { color: var(--text-muted); font-size: 14px; }
  .controls { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .search-wrap { position: relative; display: flex; align-items: center; }
  .search-input { width: 320px; max-width: 100%; padding: 8px 34px 8px 12px; border-radius: 8px; border: 1px solid var(--border); background: var(--input-bg); color: var(--text-strong); font-size: 14px; outline: none; transition: border-color 0.15s ease, box-shadow 0.15s ease; }
  .search-input::placeholder { color: var(--text-muted); }
  .search-input:focus { border-color: var(--accent); box-shadow: 0 0 0 2px rgba(56, 189, 248, 0.2); }
  .search-clear { position: absolute; right: 8px; border: none; background: transparent; color: var(--text-muted); font-size: 16px; line-height: 1; cursor: pointer; padding: 2px 4px; display: none; }
  .search-clear:hover { color: var(--text-strong); }
  .search-clear.visible { display: inline-flex; }
  .search-count { font-size: 13px; color: var(--text-muted); min-width: 96px; text-align: right; }
  .theme-toggle { display: inline-flex; align-items: center; gap: 6px; padding: 8px 12px; border-radius: 8px; border: 1px solid var(--border); background: var(--surface); color: var(--text-strong); font-size: 13px; font-weight: 600; cursor: pointer; box-shadow: var(--shadow); }
  .theme-toggle:hover { border-color: var(--accent); }
  .route-card { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; padding: 20px; margin-bottom: 24px; box-shadow: var(--shadow); }
  .route-card.hidden { display: none; }
  .route-header { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; border-bottom: 1px solid var(--border); padding-bottom: 12px; margin-bottom: 16px; }
  .route-title { font-size: 18px; font-weight: 700; color: var(--text-strong); font-family: monospace; }
  .badge { background: var(--badge-bg); color: var(--badge-text); padding: 4px 10px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .waves-container { display: flex; gap: 20px; overflow-x: auto; align-items: stretch; }
  .wave-col { background: var(--surface-subtle); border: 1px dashed var(--border-dashed); border-radius: 8px; padding: 12px; min-width: 240px; }
  .wave-title { font-size: 12px; text-transform: uppercase; letter-spacing: 0.05em; color: var(--accent); margin-bottom: 10px; font-weight: 600; }
  .skill-box { background: var(--surface); border-left: 4px solid var(--skill-border); border-radius: 6px; padding: 10px; margin-bottom: 10px; box-shadow: var(--shadow); }
  .skill-box:last-child { margin-bottom: 0; }
  .skill-box.is-match { background: var(--match-bg); box-shadow: inset 0 0 0 1px var(--match-ring); }
  .skill-name { font-weight: 700; color: var(--skill-name); font-family: monospace; font-size: 14px; margin-bottom: 6px; }
  .io-line { font-size: 12px; color: var(--text-code); font-family: monospace; margin-top: 3px; word-break: break-word; }
  .io-tag { color: var(--text-muted); }
  .empty-state { display: none; text-align: center; padding: 48px 20px; border: 1px dashed var(--border-dashed); border-radius: 10px; color: var(--text-muted); background: var(--surface); }
  .empty-state.visible { display: block; }
  @media (max-width: 640px) {
    body { padding: 16px; }
    .search-input { width: 100%; }
    .controls { width: 100%; }
    .search-wrap { flex: 1; }
  }
</style>
</head>
<body>
  <div class="topbar">
    <div>
      <h1>Yu · Deterministic Concurrency Topology</h1>
      <div class="subtitle">Static Startup Compilation · Atomic skills within the same wave execute concurrently across cores without locks</div>
    </div>
    <div class="controls">
      <div class="search-wrap">
        <input id="route-search" class="search-input" type="search" placeholder="Search routes, skills, types... (press /)" aria-label="Search routes, skills, or types" autocomplete="off">
        <button id="search-clear" class="search-clear" type="button" aria-label="Clear search">&times;</button>
      </div>
      <span id="search-count" class="search-count"></span>
      <button id="theme-toggle" class="theme-toggle" type="button" aria-label="Toggle color theme">Theme</button>
    </div>
  </div>
  <div id="routes-list">
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
  </div>
  <div id="empty-state" class="empty-state">No routes match your search.</div>
<script>
(function () {
  var root = document.documentElement;
  var toggleBtn = document.getElementById("theme-toggle");
  var searchInput = document.getElementById("route-search");
  var clearBtn = document.getElementById("search-clear");
  var countEl = document.getElementById("search-count");
  var emptyEl = document.getElementById("empty-state");
  var cards = Array.prototype.slice.call(document.querySelectorAll(".route-card"));

  function currentTheme() {
    return root.getAttribute("data-theme") === "light" ? "light" : "dark";
  }

  function applyTheme(theme, persist) {
    root.setAttribute("data-theme", theme);
    if (toggleBtn) {
      toggleBtn.textContent = theme === "light" ? "☀ Light" : "☾ Dark";
      toggleBtn.setAttribute("aria-pressed", theme === "light" ? "true" : "false");
    }
    if (persist) {
      try { localStorage.setItem("yu-debug-theme", theme); } catch (e) {}
    }
  }

  applyTheme(currentTheme(), false);

  if (toggleBtn) {
    toggleBtn.addEventListener("click", function () {
      applyTheme(currentTheme() === "light" ? "dark" : "light", true);
    });
  }

  function applyFilter() {
    var raw = searchInput ? searchInput.value.trim() : "";
    var terms = raw ? raw.toLowerCase().split(/\s+/).filter(Boolean) : [];
    var visible = 0;

    cards.forEach(function (card) {
      var text = card.textContent.toLowerCase();
      var matched = terms.every(function (term) { return text.indexOf(term) !== -1; });
      card.classList.toggle("hidden", !matched);
      if (matched) visible++;

      var skills = card.querySelectorAll(".skill-box");
      for (var i = 0; i < skills.length; i++) {
        var sText = skills[i].textContent.toLowerCase();
        var sMatch = terms.length > 0 && terms.every(function (term) { return sText.indexOf(term) !== -1; });
        skills[i].classList.toggle("is-match", sMatch);
      }
    });

    if (clearBtn) {
      clearBtn.classList.toggle("visible", raw.length > 0);
    }
    if (countEl) {
      countEl.textContent = terms.length > 0
        ? visible + " / " + cards.length + " routes"
        : cards.length + " route" + (cards.length === 1 ? "" : "s");
    }
    if (emptyEl) {
      emptyEl.classList.toggle("visible", cards.length > 0 && visible === 0);
    }
  }

  if (searchInput) {
    searchInput.addEventListener("input", applyFilter);
  }
  if (clearBtn) {
    clearBtn.addEventListener("click", function () {
      searchInput.value = "";
      applyFilter();
      searchInput.focus();
    });
  }

  document.addEventListener("keydown", function (e) {
    if (!searchInput) return;
    if (e.key === "/" && document.activeElement !== searchInput && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      searchInput.focus();
      searchInput.select();
    } else if (e.key === "Escape" && document.activeElement === searchInput) {
      if (searchInput.value) {
        searchInput.value = "";
        applyFilter();
      } else {
        searchInput.blur();
      }
    }
  });

  applyFilter();
})();
</script>
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
	engine.router.markInternal("GET", joinPaths("", path))
}
