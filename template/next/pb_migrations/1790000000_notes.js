/// <reference path="../pb_data/types.d.ts" />
// The example notes collection. The app server uses PocketBase as the signed-in person
// (impersonation), so these rules are what keep people to their own notes. Sign-in methods on users
// are off: the platform signs people in.
migrate((app) => {
  const users = app.findCollectionByNameOrId("users");
  users.passwordAuth.enabled = false;
  users.oauth2.enabled = false;
  users.otp.enabled = false;
  users.mfa.enabled = false;
  // Only the server (as superuser) creates users; a person can see their own record.
  users.listRule = "id = @request.auth.id";
  users.viewRule = "id = @request.auth.id";
  users.createRule = null;
  users.updateRule = null;
  users.deleteRule = null;
  app.save(users);

  const notes = new Collection({
    type: "base",
    name: "notes",
    listRule: "owner = @request.auth.id",
    viewRule: "owner = @request.auth.id",
    createRule: "@request.auth.id != '' && owner = @request.auth.id",
    updateRule: "owner = @request.auth.id",
    deleteRule: "owner = @request.auth.id",
    fields: [
      { type: "text", name: "text", required: true, max: 2000 },
      { type: "relation", name: "owner", required: true, collectionId: users.id, maxSelect: 1, cascadeDelete: true },
      { type: "autodate", name: "created", onCreate: true },
    ],
  });
  app.save(notes);
}, (app) => {
  app.delete(app.findCollectionByNameOrId("notes"));
});
