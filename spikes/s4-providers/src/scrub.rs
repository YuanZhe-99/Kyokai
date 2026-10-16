//! Fixture scrubbing: screenshots become placeholders, opaque blobs are shortened, and any
//! occurrence of a secret is replaced (defence in depth; keys never go into bodies anyway).

use serde_json::Value;

pub struct Scrubber<'a> {
    /// (base64 PNG, placeholder) pairs, e.g. the two screenshots.
    pub images: Vec<(String, &'a str)>,
    pub secrets: Vec<String>,
}

const OPAQUE: [&str; 3] = ["signature", "encrypted_content", "fallback_credit_token"];

impl Scrubber<'_> {
    pub fn scrub(&self, v: &Value) -> Value {
        match v {
            Value::String(s) => Value::String(self.scrub_str(s)),
            Value::Array(a) => Value::Array(a.iter().map(|x| self.scrub(x)).collect()),
            Value::Object(m) => Value::Object(
                m.iter()
                    .map(|(k, x)| {
                        let x = match x {
                            Value::String(s) if OPAQUE.contains(&k.as_str()) && !s.is_empty() => {
                                Value::String(format!("<{}>", k.to_uppercase()))
                            }
                            _ => self.scrub(x),
                        };
                        (k.clone(), x)
                    })
                    .collect(),
            ),
            other => other.clone(),
        }
    }

    fn scrub_str(&self, s: &str) -> String {
        let mut out = s.to_string();
        for (b64, name) in &self.images {
            if out.contains(b64.as_str()) {
                out = out.replace(b64.as_str(), &format!("<{name}>"));
            }
        }
        // Any other inline image (e.g. a zoom crop).
        if out.len() > 256 && (out.starts_with("iVBORw0KGgo") || out.starts_with("data:image/")) {
            out = match out.find("base64,") {
                Some(i) => format!("{}<IMAGE>", &out[..i + 7]),
                None => "<IMAGE>".to_string(),
            };
        }
        for secret in self.secrets.iter().filter(|s| s.len() >= 8) {
            if out.contains(secret.as_str()) {
                out = out.replace(secret.as_str(), "<REDACTED>");
            }
        }
        out
    }
}
