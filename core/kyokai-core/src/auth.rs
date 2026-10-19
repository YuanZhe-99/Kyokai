//! Password authentication with OPAQUE (doc/en-us/identity-and-access.md):
//! the host keeps one record per password slot and never sees a password
//! from a controller; the controller learns nothing about which slots exist.

use crate::store::Secrets;
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use opaque_ke::{
    CipherSuite, ClientLogin, ClientLoginFinishParameters, ClientRegistration,
    ClientRegistrationFinishParameters, CredentialFinalization, CredentialRequest,
    CredentialResponse, Identifiers, Ristretto255, ServerLogin, ServerLoginParameters,
    ServerRegistration, ServerSetup, TripleDh,
};
use rand_core::OsRng;
use serde::{Deserialize, Serialize};

/// Kyokai's OPAQUE cipher suite: Ristretto255, TripleDH with SHA-512 and
/// Argon2id (default parameters) as the key stretching function.
pub struct Suite;

impl CipherSuite for Suite {
    type OprfCs = Ristretto255;
    type KeyExchange = TripleDh<Ristretto255, sha2_10::Sha512>;
    type Ksf = opaque_ke::argon2::Argon2<'static>;
}

/// The two password slots, in the order requests and responses travel.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Slot {
    Temporary,
    Permanent,
}

/// Every slot, in wire order.
pub const SLOTS: [Slot; 2] = [Slot::Temporary, Slot::Permanent];

impl Slot {
    fn credential_id(self) -> &'static [u8] {
        match self {
            Slot::Temporary => b"kyokai/password/temporary",
            Slot::Permanent => b"kyokai/password/permanent",
        }
    }
}

const SETUP_NAME: &str = "opaque_setup";
const PERMANENT_NAME: &str = "opaque_permanent";

/// The host's OPAQUE server key and its password records.
pub struct Slots {
    setup: ServerSetup<Suite>,
    temporary: Option<ServerRegistration<Suite>>,
    permanent: Option<ServerRegistration<Suite>>,
}

/// A host's half-finished login for one slot, kept until the controller's
/// finalization arrives.
pub struct ServerHalf(Vec<(Slot, ServerLogin<Suite>)>);

impl Slots {
    /// Loads the server key (creating and storing it the first time) and the
    /// permanent password's record.
    /// Side effects: may write the secrets store.
    pub fn load(secrets: &Secrets) -> Result<Self, String> {
        let setup = match secrets.get(SETUP_NAME) {
            Some(text) => ServerSetup::deserialize(&decode(&text)?)
                .map_err(|_| "stored OPAQUE setup is unreadable")?,
            None => {
                let setup = ServerSetup::<Suite>::new(&mut OsRng);
                secrets.set(SETUP_NAME, &STANDARD.encode(setup.serialize()))?;
                setup
            }
        };
        let permanent = match secrets.get(PERMANENT_NAME) {
            Some(text) => Some(
                ServerRegistration::deserialize(&decode(&text)?)
                    .map_err(|_| "stored permanent password record is unreadable")?,
            ),
            None => None,
        };
        Ok(Slots {
            setup,
            temporary: None,
            permanent,
        })
    }

    /// Registers the temporary password (memory only).
    pub fn set_temporary(&mut self, password: &str, host_key: &[u8]) -> Result<(), String> {
        self.temporary = Some(register(&self.setup, Slot::Temporary, password, host_key)?);
        Ok(())
    }

    /// Sets or (with None) removes the permanent password, persisting its
    /// record.
    /// Side effects: rewrites the secrets store.
    pub fn set_permanent(
        &mut self,
        secrets: &Secrets,
        password: Option<&str>,
        host_key: &[u8],
    ) -> Result<(), String> {
        match password {
            Some(p) => {
                let record = register(&self.setup, Slot::Permanent, p, host_key)?;
                secrets.set(PERMANENT_NAME, &STANDARD.encode(record.serialize()))?;
                self.permanent = Some(record);
            }
            None => {
                secrets.remove(PERMANENT_NAME)?;
                self.permanent = None;
            }
        }
        Ok(())
    }

    /// Whether a permanent password is set (for the host's own interface).
    pub fn has_permanent(&self) -> bool {
        self.permanent.is_some()
    }

    /// Checks a password against the permanent one on this computer (to
    /// leave protected mode), running both sides of an OPAQUE login.
    /// Returns: whether it is the permanent password; the temporary one
    /// does not count.
    pub fn check_permanent(&self, password: &str, host_key: &[u8]) -> bool {
        const CONTEXT: &[u8] = b"kyokai/local-check/v1";
        if self.permanent.is_none() {
            return false;
        }
        let Ok((client, requests)) = client_start(password) else {
            return false;
        };
        let Ok((responses, server)) = self.respond(&requests, host_key, CONTEXT) else {
            return false;
        };
        match client.finish(password, &responses, host_key, CONTEXT) {
            Some((Slot::Permanent, done)) => {
                server.finish(Slot::Permanent, &done, host_key, CONTEXT)
            }
            _ => false,
        }
    }

    /// Answers the controller's credential requests, one per slot; an unset
    /// slot answers with a fake record indistinguishable from a real one.
    /// Inputs: `context` binds the login to the Noise handshake.
    pub fn respond(
        &self,
        requests: &[String; 2],
        host_key: &[u8],
        context: &[u8],
    ) -> Result<([String; 2], ServerHalf), String> {
        let mut responses = [String::new(), String::new()];
        let mut halves = Vec::with_capacity(2);
        for (i, slot) in SLOTS.into_iter().enumerate() {
            let request = CredentialRequest::<Suite>::deserialize(&decode(&requests[i])?)
                .map_err(|_| "malformed credential request")?;
            let record = match slot {
                Slot::Temporary => self.temporary.clone(),
                Slot::Permanent => self.permanent.clone(),
            };
            let started = ServerLogin::start(
                &mut OsRng,
                &self.setup,
                record,
                request,
                slot.credential_id(),
                server_params(host_key, context),
            )
            .map_err(|_| "credential response failed")?;
            responses[i] = STANDARD.encode(started.message.serialize());
            halves.push((slot, started.state));
        }
        Ok((responses, ServerHalf(halves)))
    }
}

impl ServerHalf {
    /// Checks the controller's finalization for the slot it claims.
    /// Returns: whether the password was right.
    pub fn finish(self, slot: Slot, finalization: &str, host_key: &[u8], context: &[u8]) -> bool {
        let Some((_, state)) = self.0.into_iter().find(|(s, _)| *s == slot) else {
            return false;
        };
        let Ok(bytes) = decode(finalization) else {
            return false;
        };
        let Ok(message) = CredentialFinalization::<Suite>::deserialize(&bytes) else {
            return false;
        };
        state
            .finish(message, server_params(host_key, context))
            .is_ok()
    }
}

/// A controller's login in progress: one OPAQUE client state per slot.
pub struct ClientHalf(Vec<ClientLogin<Suite>>);

/// Starts a login with the typed password against both slots.
/// Returns: the half to finish later and the two requests to send.
pub fn client_start(password: &str) -> Result<(ClientHalf, [String; 2]), String> {
    let mut states = Vec::with_capacity(2);
    let mut requests = [String::new(), String::new()];
    for request in &mut requests {
        let started = ClientLogin::<Suite>::start(&mut OsRng, password.as_bytes())
            .map_err(|_| "login start failed")?;
        *request = STANDARD.encode(started.message.serialize());
        states.push(started.state);
    }
    Ok((ClientHalf(states), requests))
}

impl ClientHalf {
    /// Opens the host's responses with the password; the first slot that
    /// opens yields the finalization to send.
    /// Returns: None when the password opens neither slot.
    pub fn finish(
        self,
        password: &str,
        responses: &[String; 2],
        host_key: &[u8],
        context: &[u8],
    ) -> Option<(Slot, String)> {
        for ((state, slot), response) in self.0.into_iter().zip(SLOTS).zip(responses) {
            let Ok(bytes) = decode(response) else {
                continue;
            };
            let Ok(message) = CredentialResponse::<Suite>::deserialize(&bytes) else {
                continue;
            };
            let params = ClientLoginFinishParameters::new(
                Some(context),
                Identifiers {
                    client: None,
                    server: Some(host_key),
                },
                None,
            );
            if let Ok(done) = state.finish(&mut OsRng, password.as_bytes(), message, params) {
                return Some((slot, STANDARD.encode(done.message.serialize())));
            }
        }
        None
    }
}

fn server_params<'a>(host_key: &'a [u8], context: &'a [u8]) -> ServerLoginParameters<'a, 'a> {
    ServerLoginParameters {
        context: Some(context),
        identifiers: Identifiers {
            client: None,
            server: Some(host_key),
        },
    }
}

/// Runs both halves of an OPAQUE registration locally (the host knows its
/// own passwords) and returns the record.
fn register(
    setup: &ServerSetup<Suite>,
    slot: Slot,
    password: &str,
    host_key: &[u8],
) -> Result<ServerRegistration<Suite>, String> {
    let fail = |_| "password registration failed".to_string();
    let client =
        ClientRegistration::<Suite>::start(&mut OsRng, password.as_bytes()).map_err(fail)?;
    let server =
        ServerRegistration::start(setup, client.message, slot.credential_id()).map_err(fail)?;
    let finished = client
        .state
        .finish(
            &mut OsRng,
            password.as_bytes(),
            server.message,
            ClientRegistrationFinishParameters::new(
                Identifiers {
                    client: None,
                    server: Some(host_key),
                },
                None,
            ),
        )
        .map_err(fail)?;
    Ok(ServerRegistration::finish(finished.message))
}

fn decode(text: &str) -> Result<Vec<u8>, String> {
    STANDARD.decode(text).map_err(|_| "not base64".to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn login(slots: &Slots, password: &str, context: &[u8]) -> Option<Slot> {
        let host = b"host-key";
        let (client, requests) = client_start(password).unwrap();
        let (responses, server) = slots.respond(&requests, host, context).unwrap();
        let (slot, fin) = client.finish(password, &responses, host, context)?;
        server.finish(slot, &fin, host, context).then_some(slot)
    }

    #[test]
    fn slots_accept_their_passwords_only() {
        let secrets = Secrets::memory();
        let mut slots = Slots::load(&secrets).unwrap();
        assert_eq!(login(&slots, "anything", b"ctx"), None, "no slot set");
        slots.set_temporary("k7mq2xpa", b"host-key").unwrap();
        assert_eq!(login(&slots, "k7mq2xpa", b"ctx"), Some(Slot::Temporary));
        assert_eq!(login(&slots, "wrong", b"ctx"), None);
        slots
            .set_permanent(&secrets, Some("correct horse"), b"host-key")
            .unwrap();
        assert_eq!(
            login(&slots, "correct horse", b"ctx"),
            Some(Slot::Permanent)
        );
        assert_eq!(login(&slots, "k7mq2xpa", b"ctx"), Some(Slot::Temporary));
        slots.set_permanent(&secrets, None, b"host-key").unwrap();
        assert_eq!(login(&slots, "correct horse", b"ctx"), None);
    }

    #[test]
    fn a_login_is_bound_to_its_channel() {
        let secrets = Secrets::memory();
        let mut slots = Slots::load(&secrets).unwrap();
        slots.set_temporary("k7mq2xpa", b"host-key").unwrap();
        let host = b"host-key";
        let (client, requests) = client_start("k7mq2xpa").unwrap();
        let (responses, server) = slots.respond(&requests, host, b"channel A").unwrap();
        // The controller sees another channel: the host's response no longer opens.
        assert!(
            client
                .finish("k7mq2xpa", &responses, host, b"channel B")
                .is_none()
        );
        drop(server);
    }

    #[test]
    fn the_server_key_and_permanent_record_persist() {
        let secrets = Secrets::memory();
        let mut slots = Slots::load(&secrets).unwrap();
        slots
            .set_permanent(&secrets, Some("correct horse"), b"host-key")
            .unwrap();
        let reloaded = Slots::load(&secrets).unwrap();
        assert!(reloaded.has_permanent());
        assert_eq!(
            login(&reloaded, "correct horse", b"ctx"),
            Some(Slot::Permanent)
        );
    }
}
