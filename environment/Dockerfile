FROM public.ecr.aws/x8v8d7g8/mars-base:latest
WORKDIR /app
ARG REPO_URL=https://github.com/cristianadrielbraun/gofer
ARG BASE_SHA=1e6c9f7095fce0f33069f415ffbb1d131b50434e
RUN git clone "$REPO_URL" . \
 && DEFAULT="$(git remote show origin | sed -n 's/.*HEAD branch: //p')" \
 && git checkout -B "$DEFAULT" "$BASE_SHA" \
 && git remote remove origin \
 && for b in $(git for-each-ref --format='%(refname:short)' refs/heads | grep -vx "$DEFAULT"); do git branch -D "$b" || true; done \
 && for t in $(git tag); do git merge-base --is-ancestor "$t" HEAD 2>/dev/null || git tag -d "$t"; done \
 && git reflog expire --expire=now --all \
 && git gc --prune=now
RUN go mod download
CMD ["bash"]
