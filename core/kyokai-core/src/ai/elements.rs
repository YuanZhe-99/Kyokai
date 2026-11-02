//! The element list (doc/en-us/computer-use.md, "Element list"): an
//! optional observation beside the screenshot, which stays the only one
//! required — the foreground window's visible controls as a numbered table
//! in the screenshot's coordinates, bound to that screenshot. An action may
//! name an element by number instead of a position: the number becomes the
//! element's centre before anything checks the action, and is refused once
//! the screen may have changed since the list.

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

use super::action::{Coordinates, ParseError};

/// One control as the system's accessibility tree reports it.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Element {
    /// Its role, lowercase ("button", "edit", "link").
    pub role: String,
    /// Its label.
    pub name: String,
    /// Its value (an edit's text); empty for a password field.
    pub text: String,
    /// Its bounds on the captured display, in its pixels: left, top, right,
    /// bottom.
    pub rect: [i32; 4],
}

/// The foreground window's elements, as one observation reads them.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Listed {
    /// The window's title.
    pub window: String,
    /// The program owning it, as `policy::Target::program` names it.
    pub program: Option<String>,
    pub elements: Vec<Element>,
}

/// Most elements a list holds.
pub const MAX_ELEMENTS: usize = 150;
/// Longest name or text the table shows, in characters.
const MAX_TEXT: usize = 80;
/// How the table begins (the sidecar and the history know it by this).
pub const HEADING: &str = "Elements on screen";

/// The list as the model reads it, and each element's centre in the
/// model's unit, numbered from 1 in the same order.
/// Inputs: `frame` — the captured display's size; `shot` — the
/// screenshot's; `coords` — the model's unit.
/// Returns: None when there is nothing to list.
pub fn table(
    listed: &Listed,
    frame: (u32, u32),
    shot: (u32, u32),
    coords: Coordinates,
) -> Option<(String, Vec<[i32; 2]>)> {
    if listed.elements.is_empty() || frame.0 == 0 || frame.1 == 0 {
        return None;
    }
    // Frame pixels → screenshot pixels → the model's unit.
    let unit = |x: i32, y: i32| {
        let sx = (x.clamp(0, frame.0 as i32 - 1) as u64 * shot.0 as u64 / frame.0 as u64) as u32;
        let sy = (y.clamp(0, frame.1 as i32 - 1) as u64 * shot.1 as u64 / frame.1 as u64) as u32;
        let (mx, my) = coords.in_model_units(sx, sy, shot);
        (mx as i32, my as i32)
    };
    let mut out = format!(
        "{HEADING} (window {:?}; numbers hold for this screenshot only; an action may give \"element\": N instead of a coordinate):",
        clip(&listed.window)
    );
    let mut centres = Vec::new();
    for (i, e) in listed.elements.iter().take(MAX_ELEMENTS).enumerate() {
        let (l, t) = unit(e.rect[0], e.rect[1]);
        let (r, b) = unit(e.rect[2] - 1, e.rect[3] - 1);
        out += &format!("\n{} {} {:?}", i + 1, e.role, clip(&e.name));
        if !e.text.is_empty() {
            out += &format!(" = {:?}", clip(&e.text));
        }
        out += &format!(" [{l},{t},{r},{b}]");
        centres.push([(l + r) / 2, (t + b) / 2]);
    }
    Some((out, centres))
}

/// A name or text cut to `MAX_TEXT` characters, on one line.
fn clip(s: &str) -> String {
    let one: String = s.split_whitespace().collect::<Vec<_>>().join(" ");
    match one.char_indices().nth(MAX_TEXT) {
        Some((i, _)) => format!("{}…", &one[..i]),
        None => one,
    }
}

/// Replaces an `element` number in a proposed action's input with that
/// element's centre as its `coordinate`.
/// Inputs: `centres` — the latest list's, None once it is stale.
/// Returns: an error for the model when the number is not one of the list's
/// or the list is stale; nothing changes without an `element`.
pub fn resolve(input: &mut Value, centres: Option<&[[i32; 2]]>) -> Result<(), ParseError> {
    let Some(obj) = input.as_object_mut() else {
        return Ok(());
    };
    let Some(n) = obj.remove("element") else {
        return Ok(());
    };
    let bad = |why: &str| ParseError::BadField("element", why.into());
    let n = n.as_u64().ok_or_else(|| bad("not a number"))?;
    let centres = centres.ok_or_else(|| {
        bad("the element numbers are stale after an action; take a new screenshot")
    })?;
    let c = (n as usize)
        .checked_sub(1)
        .and_then(|i| centres.get(i))
        .ok_or_else(|| bad("no element by that number in the latest list"))?;
    obj.insert("coordinate".into(), json!(c));
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn element(role: &str, name: &str, text: &str, rect: [i32; 4]) -> Element {
        Element {
            role: role.into(),
            name: name.into(),
            text: text.into(),
            rect,
        }
    }

    /// The table numbers elements from 1, maps their bounds into the
    /// screenshot's (or the grid's) unit, and clips long text.
    #[test]
    fn the_table_is_in_the_models_unit() {
        let listed = Listed {
            window: "Save As".into(),
            program: None,
            elements: vec![
                element("button", "Save", "", [1000, 800, 1200, 900]),
                element("edit", "File name:", &"x".repeat(100), [0, 0, 400, 50]),
            ],
        };
        let (text, centres) =
            table(&listed, (2000, 1000), (1000, 500), Coordinates::Pixels).unwrap();
        assert!(text.starts_with(HEADING));
        assert!(
            text.contains("\n1 button \"Save\" [500,400,599,449]"),
            "{text}"
        );
        assert!(
            text.contains(&format!(
                "\n2 edit \"File name:\" = \"{}…\"",
                "x".repeat(80)
            )),
            "{text}"
        );
        assert_eq!(centres, vec![[549, 424], [99, 12]]);
        let (_, grid) = table(&listed, (2000, 1000), (1000, 500), Coordinates::Grid1000).unwrap();
        assert_eq!(grid[0], [549, 849]);
        assert!(
            table(
                &Listed::default(),
                (2000, 1000),
                (1000, 500),
                Coordinates::Pixels
            )
            .is_none()
        );
    }

    /// An element number becomes its centre; unknown and stale numbers are
    /// refused; an input without one is left alone.
    #[test]
    fn element_numbers_resolve_to_centres() {
        let centres = [[10, 20], [30, 40]];
        let mut input = json!({"element": 2, "text": "shift"});
        resolve(&mut input, Some(&centres)).unwrap();
        assert_eq!(input, json!({"coordinate": [30, 40], "text": "shift"}));
        for (mut input, list) in [
            (json!({"element": 3}), Some(&centres[..])),
            (json!({"element": 0}), Some(&centres[..])),
            (json!({"element": "1"}), Some(&centres[..])),
            (json!({"element": 1}), None),
        ] {
            assert!(resolve(&mut input, list).is_err(), "{input}");
        }
        let mut plain = json!({"coordinate": [1, 2]});
        resolve(&mut plain, None).unwrap();
        assert_eq!(plain, json!({"coordinate": [1, 2]}));
    }
}
