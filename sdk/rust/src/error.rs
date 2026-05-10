use thiserror::Error;

#[derive(Debug, Error)]
pub enum Error {
    #[error("not found: {0}")]
    NotFound(String),

    #[error("conflict: {0}")]
    Conflict(String),

    #[error("invalid request: {0}")]
    InvalidRequest(String),

    #[error("API error (HTTP {status}): {body}")]
    Api { status: u16, body: String },

    #[error("HTTP error: {0}")]
    Http(#[from] reqwest::Error),

    #[error("JSON error: {0}")]
    Json(#[from] serde_json::Error),

    #[error("invalid duration: {0}")]
    InvalidDuration(String),
}

pub type Result<T> = std::result::Result<T, Error>;

pub(crate) fn raise_for_status(status: u16, body: String) -> std::result::Result<(), Error> {
    match status {
        200..=299 => Ok(()),
        404 => Err(Error::NotFound(body)),
        409 => Err(Error::Conflict(body)),
        400 => Err(Error::InvalidRequest(body)),
        _ => Err(Error::Api { status, body }),
    }
}
