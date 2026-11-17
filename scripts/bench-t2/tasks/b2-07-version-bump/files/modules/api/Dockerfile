FROM example.com/base/node:20
LABEL org.example.harbor.version="1.4.2"
LABEL org.example.harbor.min-compat="1.4.2"
WORKDIR /app
COPY . .
RUN npm install --omit=dev
CMD ["node", "src/server.js"]
