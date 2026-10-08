#!/usr/bin/env bash
# Thiết lập một lần: tạo repo GitHub và ghi URL của nó vào mọi manifest Argo CD.
#
# URL của repo được commit vào một cách có chủ ý, thay vì tiêm vào lúc apply.
# Dưới GitOps, toàn bộ trạng thái mong muốn của cluster phải tái dựng được chỉ
# từ repo — một URL do người nào đó tình cờ chạy script cung cấp là trạng thái
# chỉ tồn tại trong lịch sử shell của người đó.
source "$(dirname "$0")/lib.sh"

ROOT=$(repo_root)
need git

REPO_NAME="${REPO_NAME:-kalapa-platform}"
VISIBILITY="${VISIBILITY:-public}"
BRANCH="${BRANCH:-main}"

if [ -n "${GIT_REPO_URL:-}" ]; then
  url="$GIT_REPO_URL"
  info "Using the repo URL you supplied: $url"
else
  need gh
  gh auth status >/dev/null 2>&1 || die "run 'gh auth login' first"
  owner=$(gh api user --jq .login)
  url="https://github.com/${owner}/${REPO_NAME}.git"

  if gh repo view "${owner}/${REPO_NAME}" >/dev/null 2>&1; then
    ok "repo ${owner}/${REPO_NAME} already exists"
  else
    info "Creating ${owner}/${REPO_NAME} (${VISIBILITY})"
    # Để public, vì khi đó Argo CD không cần thông tin đăng nhập nào để đọc.
    # Repo private cũng được nhưng phải tạo thêm một Secret repo trong namespace
    # argocd — xem docs/04-gitops-with-argocd.md.
    gh repo create "${owner}/${REPO_NAME}" "--${VISIBILITY}" \
      --description "Kalapa platform — Kubernetes/GitOps lab" >/dev/null
    ok "created"
  fi
fi

info "Writing the repo URL into the Argo CD manifests"
# Chỉ các manifest của Argo CD mang placeholder; các chart không phụ thuộc repo.
files=$(grep -rl '__GIT_REPO_URL__\|__GIT_BRANCH__' "$ROOT/deploy" || true)
if [ -z "$files" ]; then
  warn "no placeholders left — already initialised"
else
  for f in $files; do
    # sed của BSD (macOS) cần tham số -i rỗng, còn sed của GNU thì không. Ghi ra
    # file tạm là cách tránh hẳn khác biệt đó.
    sed -e "s|__GIT_REPO_URL__|${url}|g" -e "s|__GIT_BRANCH__|${BRANCH}|g" "$f" > "$f.tmp"
    mv "$f.tmp" "$f"
    echo "    $(basename "$f")"
  done
  ok "$(echo "$files" | wc -l | tr -d ' ') files updated"
fi

info "Writing the image repository into the service charts"
if [ -n "${GHCR_OWNER:-}" ]; then
  ghcr_owner="$GHCR_OWNER"
else
  ghcr_owner=$(echo "$url" | sed -E 's|https://github.com/([^/]+)/.*|\1|' | tr '[:upper:]' '[:lower:]')
fi
for svc in gateway kyc scoring; do
  f="$ROOT/deploy/charts/$svc/values.yaml"
  sed -e "s|ghcr.io/kalapa-lab/kalapa-|ghcr.io/${ghcr_owner}/kalapa-|g" "$f" > "$f.tmp"
  mv "$f.tmp" "$f"
done
ok "images will be pulled from ghcr.io/${ghcr_owner}/kalapa-*"

cd "$ROOT"
git symbolic-ref HEAD "refs/heads/${BRANCH}" 2>/dev/null || true
git remote get-url origin >/dev/null 2>&1 || git remote add origin "$url"
git remote set-url origin "$url"

cat <<EOF

Repo wired up. Commit and push:

  git add -A
  git commit -m "Initial Kalapa platform"
  git push -u origin ${BRANCH}

Then:  make bootstrap
EOF
