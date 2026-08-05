#!/usr/bin/env python3
"""python tools/log_freq.py [LOG_DIR] [EXCLUDE] [OUTPUT]"""

import json, sys
from collections import Counter, defaultdict
from datetime import datetime
from pathlib import Path

INTERVAL_MINUTES = 1

log_dir = Path(sys.argv[1] if len(sys.argv) > 1 else "~/.local/state/infrss-server").expanduser()
exclude = sys.argv[2] if len(sys.argv) > 2 else "user method=GET"
output = sys.argv[3] if len(sys.argv) > 3 else "freq.html"
counts = Counter()
rss_errors = defaultdict(list)

for path in log_dir.rglob("*"):
    if not path.is_file():
        continue
    with path.open(encoding="utf-8", errors="ignore") as f:
        for line in f:
            try:
                t = datetime.strptime(line[:19], "%Y/%m/%d %H:%M:%S")
            except ValueError:
                continue
            t = t.replace(minute=t.minute // INTERVAL_MINUTES * INTERVAL_MINUTES, second=0)
            if "rss err " in line:
                rss_errors[t.isoformat()].append(line.rstrip())
            if exclude and exclude in line:
                continue
            counts[t.isoformat()] += 1

data = json.dumps(sorted(counts.items()))
error_data = json.dumps([
    {"value": [timestamp, len(lines)], "lines": lines}
    for timestamp, lines in sorted(rss_errors.items())
])
html = f'''<meta charset="utf-8">
<style>
  body {{ margin: 0; font-family: sans-serif; color: #202124; }}
  .chart {{ height: 600px; }}
</style>
<div id="c" class="chart"></div>
<script src="https://cdn.jsdelivr.net/npm/echarts@6/dist/echarts.min.js"></script>
<script>
  let c = echarts.init(document.querySelector('#c'));
  const escapeHtml = (text) => String(text)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');

  c.setOption({{
    title: {{ text: 'Log Frequency and RSS Request Errors', left: 'center' }},
    legend: {{ top: 32 }},
    grid: {{ top: 75, left: 60, right: 60, bottom: 80 }},
    tooltip: {{
      trigger: 'axis',
      confine: true,
      extraCssText: 'max-width: 900px; white-space: normal; overflow-wrap: anywhere;',
      formatter: (items) => {{
        const rows = items.map((item) => {{
          let row = `${{item.marker}}${{escapeHtml(item.seriesName)}}: ${{item.value[1]}}`;
          if (item.data.lines) {{
            row += '<br><br>' + item.data.lines.map(escapeHtml).join('<br>');
          }}
          return row;
        }});
        return `<strong>${{escapeHtml(items[0].axisValueLabel)}}</strong><br>` + rows.join('<br>');
      }},
    }},
    xAxis: {{ type: 'time' }},
    yAxis: [
      {{ type: 'value', name: 'Logs', minInterval: 1 }},
      {{ type: 'value', name: 'Errors', minInterval: 1 }},
    ],
    dataZoom: [
      {{ type: 'inside' }},
      {{ type: 'slider' }},
    ],
    series: [
      {{
        name: 'Log Frequency',
        type: 'bar',
        data: {data},
      }},
      {{
        name: 'RSS Request Errors',
        type: 'scatter',
        yAxisIndex: 1,
        symbolSize: (value) => Math.min(28, 10 + value[1] * 2),
        itemStyle: {{ color: '#c63c32' }},
        data: {error_data},
      }},
    ],
  }});

  onresize = () => c.resize();
</script>'''
Path(output).write_text(html, encoding="utf-8")
