FROM scratch
WORKDIR /opt/http-mock
COPY http-mock .
CMD ["./http-mock"]
