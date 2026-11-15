# afford in a container: Go binary plus the Playwright browser and its system
# libraries. Baselines and visual references are only comparable inside one
# image, so CI should pin this one.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/afford ./cmd/afford

FROM mcr.microsoft.com/playwright:v1.64.1-noble
COPY --from=build /out/afford /usr/local/bin/afford
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
WORKDIR /work
ENTRYPOINT ["afford"]
CMD ["run"]
