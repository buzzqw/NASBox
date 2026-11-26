#!/usr/bin/env bash
# Consegna completa di NASBox: versione, test, build, commit, NAS, tag, push,
# canale di aggiornamento dei client e demone sul NAS.
#
# uso (dalla copia di lavoro su un PC con Go e ssh verso il NAS):
#   tools/release_all.sh CLIENT_VERSION SERVER_VERSION "note per il canale"
#
# Il lavoro da consegnare deve essere già committato; lo script aggiunge solo il
# commit di versione. Si ferma al primo errore: se i test falliscono non rilascia.
# Regole del progetto: vedi AGENT-NOTES.md (NAS rsync 3.1.2, /tmp noexec, scp
# disabilitato, binari compilati con -trimpath -ldflags='-s -w').
set -euo pipefail

CLIENT_VERSION="${1:?versione client (es. 1.17.0)}"
SERVER_VERSION="${2:?versione server (es. 3.35.0)}"
NOTES="${3:-}"
NAS="${NAS:-andres@192.168.1.119}"
NAS_REPO=/volume1/Varie/sync-daemon
NAS_SERVER_DIR=/volume1/Varie/nasbox-server
CHANNEL_DIR=/volume1/NASBox/.nasbox-client-update-go
GH_REPO=buzzqw/NASBox
REPO="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${OUT:-$(mktemp -d)}"
DATE="$(date +%Y-%m-%d)"
nas() { ssh -q "$NAS" "$@"; }
step() { printf '\n== %s\n' "$*"; }

cd "$REPO"
[[ -z "$(git status --porcelain)" ]] || { echo "ci sono modifiche non committate: committa prima" >&2; exit 1; }
git rev-parse "v${CLIENT_VERSION}" >/dev/null 2>&1 && { echo "il tag v${CLIENT_VERSION} esiste già" >&2; exit 1; }
[[ -n "$NOTES" ]] || NOTES="Client ${CLIENT_VERSION}, server ${SERVER_VERSION}."

step "1/8 versioni e CHANGELOG"
sed -i "s/AppVersion = \"[0-9.]*\"/AppVersion = \"${CLIENT_VERSION}\"/" client-go/internal/version/version.go
sed -i "s/^var version = \"[0-9.]*\"/var version = \"${SERVER_VERSION}\"/" client-go/internal/serverd/serverd.go
CLIENT_VERSION="$CLIENT_VERSION" SERVER_VERSION="$SERVER_VERSION" DATE="$DATE" python3 - <<'PY'
import os, re
c, s, d = os.environ["CLIENT_VERSION"], os.environ["SERVER_VERSION"], os.environ["DATE"]
p = "CHANGELOG.md"
text = open(p, encoding="utf-8").read()
if "## Unreleased\n" not in text:
    raise SystemExit("CHANGELOG.md senza sezione Unreleased")
text = text.replace("## Unreleased\n", f"## Unreleased\n\n## {c} - {d}\n\nClient {c} e server {s}.\n", 1)
open(p, "w", encoding="utf-8").write(text)
p = "README.md"
text = open(p, encoding="utf-8").read()
text = re.sub(r"(\| NASBox Go client \(official\) \| \*\*)[0-9.]+(\*\* \|)", rf"\g<1>{c}\g<2>", text)
text = re.sub(r"(\| NASBox Go server \(`client-go/cmd/nasbox-server`\) \| \*\*)[0-9.]+(\*\* \|)", rf"\g<1>{s}\g<2>", text)
open(p, "w", encoding="utf-8").write(text)
PY
[[ "$(packaging/version-go.sh)" == "$CLIENT_VERSION" ]] || { echo "version-go.sh non coincide" >&2; exit 1; }

step "2/8 verifiche (vet + test completi)"
( cd client-go && gofmt -l . | (! grep .) && go vet ./... && go test ./... -count=1 )
bash -n server/sync-daemon-server.sh

step "3/8 build (client amd64, server arm64)"
( cd client-go
  go build -trimpath -ldflags='-s -w' -o "$OUT/nasbox-go-${CLIENT_VERSION}" ./cmd/nasbox
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$OUT/nasbox-server-arm64" ./cmd/nasbox-server )
"$OUT/nasbox-go-${CLIENT_VERSION}" version

step "4/8 commit di versione e invio al NAS"
git add CHANGELOG.md README.md client-go/internal/version/version.go client-go/internal/serverd/serverd.go
git commit -q -m "chore: rilascio client ${CLIENT_VERSION} e server ${SERVER_VERSION}

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
NAS_HEAD="$(nas "cd $NAS_REPO && git status --porcelain | head -1 | grep . && echo DIRTY || git rev-parse HEAD" | tail -1)"
[[ "$NAS_HEAD" != DIRTY ]] || { echo "il repo sul NAS ha modifiche non committate" >&2; exit 1; }
git merge-base --is-ancestor "$NAS_HEAD" HEAD || { echo "il NAS ($NAS_HEAD) non è un antenato di HEAD" >&2; exit 1; }
git bundle create "$OUT/release.bundle" "${NAS_HEAD}..HEAD" >/dev/null 2>&1
nas "cat > /volume1/Varie/release.bundle" < "$OUT/release.bundle"
nas "cd $NAS_REPO && git fetch -q /volume1/Varie/release.bundle HEAD && git merge --ff-only FETCH_HEAD >/dev/null && git tag v${CLIENT_VERSION} && git push origin master --tags 2>&1 | tail -3; rm -f /volume1/Varie/release.bundle"

step "5/8 demone sul NAS (${SERVER_VERSION})"
nas "cat > $NAS_SERVER_DIR/nasbox-server.new && chmod 755 $NAS_SERVER_DIR/nasbox-server.new" < "$OUT/nasbox-server-arm64"
[[ "$(sha256sum < "$OUT/nasbox-server-arm64")" == "$(nas "sha256sum < $NAS_SERVER_DIR/nasbox-server.new")" ]] || { echo "sha256 del binario server diverso" >&2; exit 1; }
nas "cd $NAS_SERVER_DIR && ./nasbox-server.new --version && ./nasbox-server.new -c $NAS_REPO/server/server.conf --chunk-fsck >/dev/null"
nas "cd $NAS_SERVER_DIR && cp -p nasbox-server nasbox-server.prev-\$(./nasbox-server --version | awk '{print \$NF}' | tr -d v) && mv nasbox-server.new nasbox-server && ./nasbox-server.sh --restart | tail -2"
sleep 3
nas "$NAS_SERVER_DIR/nasbox-server.sh --version"
nas "ps -o pid,args -C nasbox-server | grep -- '--run-foreground'" >/dev/null || { echo "il demone non è ripartito" >&2; exit 1; }
nas "cd $NAS_SERVER_DIR && ls -t nasbox-server.prev-* | tail -n +4 | xargs -r rm -f"

step "6/8 canale di aggiornamento dei client (go-stable)"
nas "cat > $NAS_REPO/nasbox-go-${CLIENT_VERSION} && chmod 755 $NAS_REPO/nasbox-go-${CLIENT_VERSION}" < "$OUT/nasbox-go-${CLIENT_VERSION}"
nas "cd $NAS_REPO && python3 tools/publish_go_client.py --binary ./nasbox-go-${CLIENT_VERSION} --dest-dir $CHANNEL_DIR --notes '${NOTES//\'/}' --keep 2 | tail -6; rm -f ./nasbox-go-${CLIENT_VERSION}"
nas "python3 -c \"import json,hashlib;m=json.load(open('$CHANNEL_DIR/manifest-go.json'));p='$CHANNEL_DIR/'+m['artifact'];ok=m['sha256']==hashlib.sha256(open(p,'rb').read()).hexdigest();print('canale',m['version'],'coerente' if ok else 'INCOERENTE');raise SystemExit(0 if ok else 1)\""

step "7/8 GitHub (workflow e release)"
sleep 8
RUN_ID=""
for _ in 1 2 3 4 5 6; do
  RUN_ID="$(gh run list -R "$GH_REPO" --workflow 'Package NASBox' --branch "v${CLIENT_VERSION}" -L 1 --json databaseId -q '.[0].databaseId' 2>/dev/null || true)"
  [[ -n "$RUN_ID" ]] && break; sleep 5
done
[[ -n "$RUN_ID" ]] && timeout 900 gh run watch "$RUN_ID" -R "$GH_REPO" --exit-status >/dev/null && echo "workflow Package: ok"
gh release view "v${CLIENT_VERSION}" -R "$GH_REPO" --json assets -q '[.assets[].name]|join(", ")'

step "8/8 stato finale"
nas "cd $NAS_REPO && git status -sb | head -2"
echo "CONSEGNA COMPLETATA: client ${CLIENT_VERSION}, server ${SERVER_VERSION}"
