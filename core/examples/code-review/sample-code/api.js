// Sample API code with intentional issues for review

const express = require('express');
const app = express();

// SECURITY ISSUE: Hardcoded credentials
const DB_PASSWORD = "admin123";
const API_KEY = "sk-1234567890abcdef";

// SECURITY ISSUE: SQL injection vulnerability
app.get('/user/:id', (req, res) => {
  const userId = req.params.id;
  const query = `SELECT * FROM users WHERE id = ${userId}`;
  db.query(query, (err, results) => {
    res.json(results);
  });
});

// SECURITY ISSUE: XSS vulnerability
app.get('/search', (req, res) => {
  const searchTerm = req.query.q;
  res.send(`<h1>Results for: ${searchTerm}</h1>`);
});

// PERFORMANCE ISSUE: N+1 query problem
app.get('/posts', async (req, res) => {
  const posts = await db.query('SELECT * FROM posts');
  for (let post of posts) {
    post.author = await db.query(`SELECT * FROM users WHERE id = ${post.author_id}`);
    post.comments = await db.query(`SELECT * FROM comments WHERE post_id = ${post.id}`);
  }
  res.json(posts);
});

// PERFORMANCE ISSUE: Inefficient nested loops
function findDuplicates(arr1, arr2) {
  const duplicates = [];
  for (let i = 0; i < arr1.length; i++) {
    for (let j = 0; j < arr2.length; j++) {
      if (arr1[i] === arr2[j]) {
        duplicates.push(arr1[i]);
      }
    }
  }
  return duplicates;
}

// STYLE ISSUE: Long line
app.post('/webhook', (req, res) => { const data = req.body; const result = processWebhook(data.event, data.payload, data.signature, data.timestamp, data.user_id); res.json(result); });

// STYLE ISSUE: Inconsistent naming
function ProcessData(input_value) {
  const TempResult = input_value.map(x => x * 2);
  return TempResult;
}

// STYLE ISSUE: Missing error handling
app.get('/file/:name', (req, res) => {
  const content = fs.readFileSync(`./uploads/${req.params.name}`);
  res.send(content);
});

module.exports = app;
