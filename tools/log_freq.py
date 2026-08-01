#!/usr/bin/env python3
"""python tools/log_freq.py [LOG_DIR] [EXCLUDE] [OUTPUT]"""

import json, sys
from collections import Counter
from datetime import datetime
from pathlib import Path

INTERVAL_MINUTES = 1

log_dir = Path(sys.argv[1] if len(sys.argv) > 1 else "~/.local/state/infrss-server").expanduser()
exclude = sys.argv[2] if len(sys.argv) > 2 else "user method=GET"
output = sys.argv[3] if len(sys.argv) > 3 else "freq.html"
counts = Counter()

for path in log_dir.rglob("*"):
    if not path.is_file():
        continue
    with path.open(encoding="utf-8", errors="ignore") as f:
        for line in f:
            if exclude and exclude in line:
                continue
            try:
                t = datetime.strptime(line[:19], "%Y/%m/%d %H:%M:%S")
            except ValueError:
                continue
            t = t.replace(minute=t.minute // INTERVAL_MINUTES * INTERVAL_MINUTES, second=0)
            counts[t.isoformat()] += 1

data = json.dumps(sorted(counts.items()))
html = f'''<meta charset="utf-8">
<div id="c" style="height: 500px"></div>
<script src="https://cdn.jsdelivr.net/npm/echarts@6/dist/echarts.min.js"></script>
<script>
  let c = echarts.init(document.querySelector('#c'));
  c.setOption({{
    tooltip: {{
      trigger: 'axis',
    }},
    xAxis: {{
      type: 'time',
    }},
    yAxis: {{
      type: 'value',
    }},
    dataZoom: [
      {{ type: 'inside' }},
      {{ type: 'slider' }},
    ],
    series: [
      {{
        type: 'bar',
        data: {data},
      }},
    ],
  }});
  onresize = () => c.resize();
</script>'''
Path(output).write_text(html, encoding="utf-8")
