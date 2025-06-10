FROM golang:1.22.3

ARG COMPONENT=tc2-hat-comms

WORKDIR /src
COPY . .
RUN mkdir -p /out

ENV GOOS=linux
ENV GOARCH=arm64

COPY <<-EOT script.sh
go build -o /out/${COMPONENT} /src/cmd/${COMPONENT}
EOT

CMD ["bash", "/src/script.sh"]


