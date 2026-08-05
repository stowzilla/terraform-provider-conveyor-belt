require 'models/user'

class Database
  def self.find_user(id)
    # Mock implementation
    User.new(id: id, name: "Test User", email: "test@example.com")
  end

  def self.save_user(user)
    # Mock implementation
    puts "Saving user: #{user.name}"
    true
  end
end