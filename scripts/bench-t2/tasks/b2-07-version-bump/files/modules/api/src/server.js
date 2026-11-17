const express = require('express-lite');
const app = express();

app.get('/health', (req, res) => res.json({ ok: true }));

app.listen(8080);
