# Accounts and app visibility

A box can have more than one account. The owner keeps full control, and decides what the other accounts see: nothing beyond their own account and the apps the owner leaves visible, unless the owner grants more. This page says what that does, and what it does not.

## Accounts

- The first account of the box is the **owner**. Its role is `admin`. It sees everything and can do everything, as before.
- Every other account has the role `user`. There is only one `admin`: no account can be given that role, and no account can change its own role.
- A `user` is **read-only**, everywhere on the box, and **closed by default**: whatever is not listed below is refused.

### Create and delete an account

Only the owner can do this, in **Settings → Accounts**.

- **Create**: give a name and a password. The password follows the same rules as the first account's. The new account sees its own account and the visible apps, and has no module right.
- **Delete**: the account is gone at once, with its sessions, its folder and its module rights. The box then removes the account from the hidden lists, which AppManagement keeps. If AppManagement does not answer, the account is deleted all the same and the screen says the hidden lists were not cleaned. What is left there names an account that no longer exists, so it hides nothing from anyone; saving an app's **Visibility** tab drops it from that app's list. The owner cannot delete their own account.

Every account gets a number of its own, never given to another account, even after the account is deleted. A new account, even one with the name of a deleted one, starts clean: it inherits neither the hidden apps, nor the rights, nor the sessions of the old one.

## What a `user` sees by default

- Their own account: they can change their avatar, password, two-factor authentication and display preferences (search bar, language, appearance, background, app launching). Their login name is the one the owner gave.
- The apps that are visible to them: the dashboard, the apps list, and the apps themselves.
- **Statistics** and **Logs** of the visible apps.

Nothing else is shown. There are no files, no terminal and no system-log icon in the top bar, and no virtual machine console. The dashboard hides the controls that change anything (install, uninstall, update, start, stop, settings, backups, app stores, accounts); the box refuses them anyway. An app that is stopped is shown as stopped, with a text saying that only the owner can start it.

## Module rights

The owner can let an account **read** a module. In **Settings → Accounts**, each account has one checkbox per module. A new account has none ticked.

| Right | What it opens, read-only |
|---|---|
| `storage` | disks and their health |
| `vm` | the virtual machines page: list, state and IP address of each machine |
| `backups` | state and history of the backups |
| `fleet` | the list of the boxes of the fleet |
| `system` | load and utilization of the box |

How it shows in the interface:

- The new dashboard is offered to an account as soon as one module is ticked. Each module shows there with its own right: the storage, backups and fleet cards, the virtual machines' tiles, and, with `system`, the load, memory, disk space and network of the box.
- The Fleet page, and its card among the apps, are for the owner only, even when `fleet` is ticked: the account reads the list of the boxes on the new dashboard.
- The virtual machines page lists the machines with their state and IP address. There is no console, no way to start or stop a machine, and no files. Where a machine's disks, its ISO and its shared folders are on the box is not given to the account.
- `system` does not open the hardware details or the OS version; it opens the load and utilization of the box. The terminal and the raw system logs stay with the owner.
- A module with no right has no card and no menu entry.

If you untick a right, the account loses it within seconds; the cards already on a screen go away at the next reload.

## Hide an app for an account

Only the owner can do this. Open the app's settings and go to the **Visibility** tab (shown to the owner only): there is one box per `user` account, "Hidden for <name>". Tick the accounts, then Save.

- A new app is visible to everyone until you hide it.
- Updating or reinstalling an app keeps its hidden list.
- **Uninstalling does not clear it.** The logs, statistics and backups of an app outlive it, so the entry stays, and a reinstall of the same name is still hidden. It stays until the owner removes it: **Settings → Accounts** lists the entries that no longer match an installed app, and each one can be removed there.
- The owner sees every app, hidden or not.

## What "hidden" means

For an account the app is hidden for, the app is absent from the lists of the box (dashboard, apps list, Statistics, Logs, operations, backups) and its routes on the box answer "not found", as if it did not exist. Hiding applies to apps only: the box itself and the virtual machines are not hidden by it, and show wherever the account may read them. A container started outside an app is the owner's alone: no other account sees it, whatever its rights.

**Hidden is not locked.** An app listens on its own port, and that port is not behind the box's sign-in. Hiding means the box hides the app from its lists; it does not block the app's own address. Anyone who knows the address of the app can still open it.

## Live updates

A `user` does not receive the box's message bus. The lists on screen refresh on their own every 10 seconds instead.

## Secrets of apps

For a `user`, the values of environment variables, the content of the `.env` file, webhook secrets and global settings are masked in what the box shows. The names of the variables stay visible.

## When the box says no

Whatever the box refuses, a `user` sees one translated message, not a raw error. A `user` is refused at the box's entrance (the Gateway) for every change, and for every read that is neither their own account, a visible app, nor a module the owner ticked.

The owner is not refused what a `user` is refused. One exception: when the box's entrance has just restarted and the account service is not answering, the box cannot tell who is the owner, and answers "unavailable" to every account, the owner's included, until the account service is back.

## Phones and PCs on remote access

A phone or PC that joins the box over remote access has no account of its own: it reaches the dashboard and signs in with a normal account, and gets that account's rights.

## Wording in translations

The interface says **hidden**, never "forbidden". Translations must keep that meaning: the word chosen for a translated string must say that the app is hidden from the list, not that access is prohibited.
