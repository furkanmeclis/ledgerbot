# Grafana with ledgerbot's data sources and dashboard provisioned. Baked into
# the image because Dokploy re-clones the repository on every deploy, so repo
# files cannot be bind-mounted.
FROM grafana/grafana:12.1.0
COPY datasources.yml /etc/grafana/provisioning/datasources/ledgerbot.yml
COPY dashboards.yml /etc/grafana/provisioning/dashboards/ledgerbot.yml
COPY dashboards /var/lib/grafana-dashboards/ledgerbot
