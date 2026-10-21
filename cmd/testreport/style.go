package main

const css = `
:root{color-scheme:light;--bg:#f6f6f4;--surface:#fcfcfb;--border:#e4e3df;--text:#0b0b0b;--text2:#52514e;--muted:#8a8984;
--s1:#2a78d6;--s2:#eb6834;--s3:#1baf7a;--s4:#eda100;--s5:#e87ba4;--s6:#008300;--s7:#6250d6;--s8:#e34948;
--good:#0ca30c;--bad:#d03b3b;--warn:#fab219;--goodtext:#006300;--badtext:#b42323}
@media (prefers-color-scheme:dark){:root:not([data-theme="light"]){color-scheme:dark;--bg:#111110;--surface:#1a1a19;--border:#2e2e2c;--text:#f0efec;--text2:#c3c2b7;--muted:#8d8c86;
--s1:#3987e5;--s2:#d95926;--s3:#199e70;--s4:#c98500;--s5:#d55181;--s6:#008300;--s7:#9085e9;--s8:#e66767;
--good:#2bb32b;--bad:#e05252;--warn:#e0a020;--goodtext:#5fd35f;--badtext:#ff8080}}
:root[data-theme="dark"]{color-scheme:dark;--bg:#111110;--surface:#1a1a19;--border:#2e2e2c;--text:#f0efec;--text2:#c3c2b7;--muted:#8d8c86;
--s1:#3987e5;--s2:#d95926;--s3:#199e70;--s4:#c98500;--s5:#d55181;--s6:#008300;--s7:#9085e9;--s8:#e66767;
--good:#2bb32b;--bad:#e05252;--warn:#e0a020;--goodtext:#5fd35f;--badtext:#ff8080}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,sans-serif}
main{max-width:1180px;margin:0 auto;padding:24px 16px 64px}
header{display:flex;justify-content:space-between;align-items:flex-start;gap:16px;margin-bottom:20px}
h1{font-size:24px;margin:0 0 4px}h2{font-size:19px;margin:32px 0 6px}h3{font-size:15px;margin:22px 0 8px}h4{margin:0 0 2px;font-size:14px}
.sub{color:var(--text2);margin:0 0 10px}.small{color:var(--text2);font-size:12.5px}
code{font:12.5px ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:0 4px}
.verdict{font-weight:700;padding:6px 14px;border-radius:999px;color:#fff;white-space:nowrap}
.verdict.good{background:var(--good)}.verdict.bad{background:var(--bad)}.verdict.warn{background:var(--warn);color:#111}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px}
.tile,.card{background:var(--surface);border:1px solid var(--border);border-radius:10px;padding:14px 16px}
.tile .label{color:var(--text2);font-size:12.5px}.tile .value{font-size:26px;font-weight:650;margin:2px 0}.tile .note{color:var(--muted);font-size:12px}
.card{margin:12px 0}.grid2{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,520px),1fr));gap:12px}.grid2 .card{margin:0}
.findings li{margin:4px 0}
table{width:100%;border-collapse:collapse;margin:8px 0 4px;background:var(--surface);border:1px solid var(--border);border-radius:8px;overflow:hidden;display:block;overflow-x:auto}
th,td{padding:6px 10px;border-bottom:1px solid var(--border);text-align:left;vertical-align:top}
th{color:var(--text2);font-weight:600;font-size:12.5px;white-space:nowrap}
td.n,th.n{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
.badtext{color:var(--badtext);font-weight:600}
table.cov td{padding:3px 10px}.barcell{width:50%}.barcell span{display:block;height:10px;border-radius:0 4px 4px 0;background:var(--s1)}
details{margin:6px 0;background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:8px 12px}
details.fail summary{color:var(--badtext)}details summary{cursor:pointer}
pre{white-space:pre-wrap;font:12px/1.45 ui-monospace,Menlo,monospace;max-height:420px;overflow:auto;margin:8px 0 0}
.legend{display:flex;flex-wrap:wrap;gap:6px 16px;font-size:12.5px;color:var(--text2);margin:6px 0}
.legend i{display:inline-block;width:10px;height:10px;border-radius:3px;margin-right:6px;vertical-align:-1px}
.legend i.good,rect.good{background:var(--good);fill:var(--good)}.legend i.bad,rect.bad{background:var(--bad);fill:var(--bad)}.legend i.muted,rect.muted{background:var(--muted);fill:var(--muted)}
svg.chart{width:100%;max-width:820px;height:auto;display:block}
svg .grid{stroke:var(--border);stroke-width:1}svg .ref{stroke:var(--bad);stroke-dasharray:4 4}
svg .tick{fill:var(--text2);font-size:11px}svg .axis{fill:var(--muted);font-size:11px}
svg .dlabel{fill:var(--text);font-size:11px;font-weight:600}
svg .dot{stroke:var(--surface);stroke-width:2}svg .dot:hover,svg .bar:hover{opacity:.8}
@media (max-width:640px){header{flex-direction:column}h1{font-size:20px}}
`
